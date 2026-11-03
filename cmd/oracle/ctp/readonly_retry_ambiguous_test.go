package ctp

import (
	"context"
	"errors"
	"testing"
)

// 复合错误含未知原因，不能挑出其中一个确认超时就重试。
func TestReadOnlyParamsRetryAmbiguousFailure(t *testing.T) {
	f := newRetryFixture()
	f.connects = []error{errors.Join(confirmTimeout(), errors.New("unknown lifecycle failure")), nil}
	_, err := queryBrokerParamsReadOnly(context.Background(), retryCredentials(), retryPolicy(), f.runtime(t), nil)
	if err == nil || len(f.clients) != 1 {
		t.Fatal("复合未知错误被当确认超时重试")
	}
}
