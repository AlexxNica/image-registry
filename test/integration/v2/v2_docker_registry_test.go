package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"testing"

	"github.com/docker/distribution/context"
	"github.com/docker/distribution/digest"
	"github.com/docker/distribution/manifest"
	"github.com/docker/distribution/manifest/schema1"
	_ "github.com/docker/distribution/registry/storage/driver/inmemory"
	"github.com/docker/libtrust"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/origin/pkg/cmd/util/tokencmd"
	imageapi "github.com/openshift/origin/pkg/image/apis/image"
	imageclient "github.com/openshift/origin/pkg/image/generated/internalclientset"

	registryutil "github.com/openshift/image-registry/pkg/dockerregistry/testutil"
	"github.com/openshift/image-registry/pkg/testframework"
)

// gzippedEmptyTar is a gzip-compressed version of an empty tar file
// (1024 NULL bytes)
var gzippedEmptyTar = []byte{
	31, 139, 8, 0, 0, 9, 110, 136, 0, 255, 98, 24, 5, 163, 96, 20, 140, 88,
	0, 8, 0, 0, 255, 255, 46, 175, 181, 239, 0, 4, 0, 0,
}

// digestSHA256GzippedEmptyTar is the canonical sha256 digest of
// gzippedEmptyTar
const digestSHA256GzippedEmptyTar = digest.Digest("sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4")

func signedManifest(name string, blobs []digest.Digest) ([]byte, digest.Digest, error) {
	key, err := libtrust.GenerateECP256PrivateKey()
	if err != nil {
		return []byte{}, "", fmt.Errorf("error generating EC key: %s", err)
	}

	history := make([]schema1.History, 0, len(blobs))
	fsLayers := make([]schema1.FSLayer, 0, len(blobs))
	for _, b := range blobs {
		history = append(history, schema1.History{V1Compatibility: `{"id": "foo"}`})
		fsLayers = append(fsLayers, schema1.FSLayer{BlobSum: b})
	}

	mappingManifest := schema1.Manifest{
		Versioned: manifest.Versioned{
			SchemaVersion: 1,
		},
		Name:         name,
		Tag:          imageapi.DefaultImageTag,
		Architecture: "amd64",
		History:      history,
		FSLayers:     fsLayers,
	}

	manifestBytes, err := json.MarshalIndent(mappingManifest, "", "    ")
	if err != nil {
		return []byte{}, "", fmt.Errorf("error marshaling manifest: %s", err)
	}
	dgst := digest.FromBytes(manifestBytes)

	jsonSignature, err := libtrust.NewJSONSignature(manifestBytes)
	if err != nil {
		return []byte{}, "", fmt.Errorf("error creating json signature: %s", err)
	}

	if err = jsonSignature.Sign(key); err != nil {
		return []byte{}, "", fmt.Errorf("error signing manifest: %s", err)
	}

	signedBytes, err := jsonSignature.PrettySignature("signatures")
	if err != nil {
		return []byte{}, "", fmt.Errorf("error invoking PrettySignature: %s", err)
	}

	return signedBytes, dgst, nil
}

func TestV2RegistryGetTags(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "image-registry-test-integration-")
	if err != nil {
		t.Fatalf("failed to create temporary directory: %s", err)
	}
	defer os.RemoveAll(tmpDir)

	configDir := path.Join(tmpDir, "config")
	adminKubeConfigPath := path.Join(configDir, "master", "admin.kubeconfig")

	masterContainer, err := testframework.StartMasterContainer(configDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := masterContainer.Stop(); err != nil {
			t.Log(err)
		}
	}()

	clusterAdminClientConfig, err := testframework.ConfigFromFile(adminKubeConfigPath)
	if err != nil {
		t.Fatal(err)
	}

	namespace := "namespace"
	user := "admin"
	password := "password"

	if err := testframework.CreateProject(clusterAdminClientConfig, namespace, user); err != nil {
		t.Fatal(err)
	}

	token, err := tokencmd.RequestToken(clusterAdminClientConfig, nil, user, password)
	if err != nil {
		t.Fatalf("error requesting token: %v", err)
	}

	registryAddr, err := testframework.StartTestRegistry(t, adminKubeConfigPath)
	if err != nil {
		t.Fatalf("start registry: %v", err)
	}

	adminImageClient := imageclient.NewForConfigOrDie(clusterAdminClientConfig)

	stream := imageapi.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      "test",
		},
	}
	if _, err := adminImageClient.ImageStreams(namespace).Create(&stream); err != nil {
		t.Fatalf("error creating image stream: %s", err)
	}

	tags, err := getTags(registryAddr, namespace, stream.Name, user, token)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) > 0 {
		t.Fatalf("expected 0 tags, got: %#v", tags)
	}

	err = putEmptyBlob(registryAddr, namespace, stream.Name, user, token)
	if err != nil {
		t.Fatal(err)
	}

	dgst, err := putManifest(registryAddr, namespace, stream.Name, user, token)
	if err != nil {
		t.Fatal(err)
	}

	tags, err = getTags(registryAddr, namespace, stream.Name, user, token)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d: %v", len(tags), tags)
	}
	if tags[0] != imageapi.DefaultImageTag {
		t.Fatalf("expected latest, got %q", tags[0])
	}

	// test get by tag
	url := fmt.Sprintf("http://%s/v2/%s/%s/manifests/%s", registryAddr, namespace, stream.Name, imageapi.DefaultImageTag)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("error creating request: %v", err)
	}
	req.SetBasicAuth(user, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("error retrieving manifest from registry: %s", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status code: %d", resp.StatusCode)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("error retrieving manifest: %v", err)
	}
	var retrievedManifest schema1.Manifest
	if err := json.Unmarshal(body, &retrievedManifest); err != nil {
		t.Fatalf("error unmarshaling retrieved manifest: %v", err)
	}
	if retrievedManifest.Name != fmt.Sprintf("%s/%s", namespace, stream.Name) {
		t.Fatalf("unexpected manifest name: %s", retrievedManifest.Name)
	}
	if retrievedManifest.Tag != imageapi.DefaultImageTag {
		t.Fatalf("unexpected manifest tag: %s", retrievedManifest.Tag)
	}

	// test get by digest
	url = fmt.Sprintf("http://%s/v2/%s/%s/manifests/%s", registryAddr, namespace, stream.Name, dgst.String())
	req, err = http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("error creating request: %v", err)
	}
	req.SetBasicAuth(user, token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("error retrieving manifest from registry: %s", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status code: %d", resp.StatusCode)
	}
	body, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("error retrieving manifest: %v", err)
	}
	if err := json.Unmarshal(body, &retrievedManifest); err != nil {
		t.Fatalf("error unmarshaling retrieved manifest: %v", err)
	}
	if retrievedManifest.Name != fmt.Sprintf("%s/%s", namespace, stream.Name) {
		t.Fatalf("unexpected manifest name: %s", retrievedManifest.Name)
	}
	if retrievedManifest.Tag != imageapi.DefaultImageTag {
		t.Fatalf("unexpected manifest tag: %s", retrievedManifest.Tag)
	}

	image, err := adminImageClient.ImageStreamImages(namespace).Get(imageapi.JoinImageStreamImage(stream.Name, dgst.String()), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("error getting imageStreamImage: %s", err)
	}
	if e, a := fmt.Sprintf("test@%s", dgst.String()), image.Name; e != a {
		t.Errorf("image name: expected %q, got %q", e, a)
	}
	if e, a := dgst.String(), image.Image.Name; e != a {
		t.Errorf("image name: expected %q, got %q", e, a)
	}
	if e, a := fmt.Sprintf("%s/%s/%s@%s", registryAddr, namespace, stream.Name, dgst.String()), image.Image.DockerImageReference; e != a {
		t.Errorf("image dockerImageReference: expected %q, got %q", e, a)
	}
	if e, a := "foo", image.Image.DockerImageMetadata.ID; e != a {
		t.Errorf("image dockerImageMetadata.ID: expected %q, got %q", e, a)
	}

	// test auto provisioning
	otherStream, err := adminImageClient.ImageStreams(namespace).Get("otherrepo", metav1.GetOptions{})
	t.Logf("otherStream=%#v, err=%v", otherStream, err)
	if err == nil {
		t.Fatalf("expected error getting otherrepo")
	}

	err = putEmptyBlob(registryAddr, namespace, "otherrepo", user, token)
	if err != nil {
		t.Fatal(err)
	}

	otherDigest, err := putManifest(registryAddr, namespace, "otherrepo", user, token)
	if err != nil {
		t.Fatal(err)
	}

	otherStream, err = adminImageClient.ImageStreams(namespace).Get("otherrepo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("unexpected error getting otherrepo: %s", err)
	}
	if otherStream == nil {
		t.Fatalf("unexpected nil otherrepo")
	}
	if len(otherStream.Status.Tags) != 1 {
		t.Errorf("expected 1 tag, got %#v", otherStream.Status.Tags)
	}
	history, ok := otherStream.Status.Tags[imageapi.DefaultImageTag]
	if !ok {
		t.Fatal("unable to find 'latest' tag")
	}
	if len(history.Items) != 1 {
		t.Errorf("expected 1 tag event, got %#v", history.Items)
	}
	if e, a := otherDigest.String(), history.Items[0].Image; e != a {
		t.Errorf("digest: expected %q, got %q", e, a)
	}
}

func putManifest(registryAddr, namespace, name, user, token string) (digest.Digest, error) {
	creds := registryutil.NewBasicCredentialStore(user, token)
	desc, _, err := registryutil.UploadRandomTestBlob(context.Background(), &url.URL{Host: registryAddr, Scheme: "http"}, creds, namespace+"/"+name)
	if err != nil {
		return "", err
	}

	putUrl := fmt.Sprintf("http://%s/v2/%s/%s/manifests/%s", registryAddr, namespace, name, imageapi.DefaultImageTag)
	signedManifest, dgst, err := signedManifest(fmt.Sprintf("%s/%s", namespace, name), []digest.Digest{desc.Digest})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("PUT", putUrl, bytes.NewReader(signedManifest))
	if err != nil {
		return "", fmt.Errorf("error creating put request: %s", err)
	}
	req.SetBasicAuth(user, token)
	client := http.DefaultClient
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error putting manifest: %s", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("unexpected put status code: %d", resp.StatusCode)
	}
	return dgst, nil
}

func putEmptyBlob(registryAddr, namespace, name, user, token string) error {
	putUrl := fmt.Sprintf("http://%s/v2/%s/%s/blobs/uploads/", registryAddr, namespace, name)
	method := "POST"

	for range []int{1, 2} {
		req, err := http.NewRequest(method, putUrl, bytes.NewReader(gzippedEmptyTar))
		if err != nil {
			return fmt.Errorf("error makeing request: %s", err)
		}
		req.SetBasicAuth(user, token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("error posting blob: %s", err)
		}
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusAccepted:
			putUrl = resp.Header.Get("Location") + "&digest=" + digestSHA256GzippedEmptyTar.String()
			method = "PUT"
		case http.StatusCreated:
			return nil
		default:
			return fmt.Errorf("unexpected post status code: %d", resp.StatusCode)
		}
	}

	return nil
}

func getTags(registryAddr, namespace, streamName, user, token string) ([]string, error) {
	url := fmt.Sprintf("http://%s/v2/%s/%s/tags/list", registryAddr, namespace, streamName)
	client := http.DefaultClient
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return []string{}, fmt.Errorf("error creating request: %v", err)
	}
	req.SetBasicAuth(user, token)
	resp, err := client.Do(req)
	if err != nil {
		return []string{}, fmt.Errorf("error retrieving tags from registry: %s", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return []string{}, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return []string{}, fmt.Errorf("error retrieving manifest: %v", err)
	}
	m := make(map[string]interface{})
	err = json.Unmarshal(body, &m)
	if err != nil {
		return []string{}, fmt.Errorf("error unmarhsaling response %q: %s", body, err)
	}
	arr, ok := m["tags"].([]interface{})
	if !ok {
		return []string{}, fmt.Errorf("couldn't convert tags")
	}
	tags := []string{}
	for _, value := range arr {
		tag, ok := value.(string)
		if !ok {
			return []string{}, fmt.Errorf("tag %#v is not a string", value)
		}
		tags = append(tags, tag)
	}
	return tags, nil
}
