//go:build darwin

package secrets

import (
	"errors"
	"testing"
)

func TestLoadSecurityFrameworkKeychain(t *testing.T) {
	api, err := loadSecurityFrameworkKeychain()
	if err != nil {
		t.Fatal(err)
	}
	if api.secClass == 0 || api.secClassGenericPassword == 0 || api.cfBooleanTrue == 0 {
		t.Fatal("Security.framework bindings contain a nil constant")
	}
}

func TestClassifySecurityStatus(t *testing.T) {
	for _, status := range []int32{-25308, -25293, -128} {
		if err := classifySecurityStatus(status); !errors.Is(err, ErrBackendLocked) {
			t.Fatalf("status %d error = %v", status, err)
		}
	}
	if err := classifySecurityStatus(-25300); !errors.Is(err, ErrNotFound) {
		t.Fatalf("not-found error = %v", err)
	}
	if err := classifySecurityStatus(0); err != nil {
		t.Fatalf("success error = %v", err)
	}
}
