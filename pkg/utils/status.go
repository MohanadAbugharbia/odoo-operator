package utils

import (
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func UpdateStatus(conditions *[]metav1.Condition, statusType string, statusReason string, statusMessage string, condition metav1.ConditionStatus) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               statusType,
		Status:             condition,
		Reason:             statusReason,
		LastTransitionTime: metav1.NewTime(time.Now()),
		Message:            statusMessage,
	})
}
