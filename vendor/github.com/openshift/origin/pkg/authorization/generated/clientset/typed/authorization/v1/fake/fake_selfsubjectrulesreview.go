package fake

import (
	v1 "github.com/openshift/origin/pkg/authorization/apis/authorization/v1"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	testing "k8s.io/client-go/testing"
)

// FakeSelfSubjectRulesReviews implements SelfSubjectRulesReviewInterface
type FakeSelfSubjectRulesReviews struct {
	Fake *FakeAuthorizationV1
	ns   string
}

var selfsubjectrulesreviewsResource = schema.GroupVersionResource{Group: "authorization.openshift.io", Version: "v1", Resource: "selfsubjectrulesreviews"}

var selfsubjectrulesreviewsKind = schema.GroupVersionKind{Group: "authorization.openshift.io", Version: "v1", Kind: "SelfSubjectRulesReview"}

// Create takes the representation of a selfSubjectRulesReview and creates it.  Returns the server's representation of the selfSubjectRulesReview, and an error, if there is any.
func (c *FakeSelfSubjectRulesReviews) Create(selfSubjectRulesReview *v1.SelfSubjectRulesReview) (result *v1.SelfSubjectRulesReview, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewCreateAction(selfsubjectrulesreviewsResource, c.ns, selfSubjectRulesReview), &v1.SelfSubjectRulesReview{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1.SelfSubjectRulesReview), err
}
