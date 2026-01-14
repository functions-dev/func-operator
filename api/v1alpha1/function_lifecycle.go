package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	FunctionConditionDeployed = "Deployed"
)

func (s *FunctionStatus) MarkDeploySucceeded() bool {
	return meta.SetStatusCondition(&s.Conditions, metav1.Condition{
		Type:    FunctionConditionDeployed,
		Status:  metav1.ConditionTrue,
		Reason:  "Succeeded",
		Message: "Function has been successfully deployed",
	})
}

func (s *FunctionStatus) MarkDeployFailed(reason, messageFormat string, messageA ...interface{}) bool {
	return meta.SetStatusCondition(&s.Conditions, metav1.Condition{
		Type:    FunctionConditionDeployed,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: fmt.Sprintf(messageFormat, messageA...),
	})
}
