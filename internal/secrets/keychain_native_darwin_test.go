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
	if api.secClass == 0 || api.secClassGenericPassword == 0 || api.secAttrAccess == 0 || api.cfBooleanTrue == 0 {
		t.Fatal("Security.framework bindings contain a nil constant")
	}
}

func TestClassifySecurityStatus(t *testing.T) {
	for status, kind := range map[int32]error{
		-67869: ErrBackendLocked,
		-25308: ErrInteractionRequired,
		-25293: ErrAccessDenied,
		-25244: ErrAccessDenied,
		-128:   ErrInteractionCanceled,
		-25291: ErrBackendUnavailable,
	} {
		if err := classifySecurityStatus(status); !errors.Is(err, kind) {
			t.Fatalf("status %d error = %v, want %v", status, err, kind)
		}
	}
	if err := classifySecurityStatus(-25300); !errors.Is(err, ErrNotFound) {
		t.Fatalf("not-found error = %v", err)
	}
	if err := classifySecurityStatus(0); err != nil {
		t.Fatalf("success error = %v", err)
	}
}
