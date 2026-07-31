package telegram

import (
	"runtime"
	"strings"
	"testing"

	"github.com/ardasevinc/tele/internal/buildinfo"
)

func TestRuntimeDeviceConfigDescribesActualPlatform(t *testing.T) {
	device := runtimeDeviceConfig()
	if device.DeviceModel != "tele" || device.AppVersion != buildinfo.Version {
		t.Fatalf("device identity = %+v", device)
	}
	if !strings.HasSuffix(device.SystemVersion, "/"+runtime.GOARCH) {
		t.Fatalf("system version %q omits runtime architecture", device.SystemVersion)
	}
	if runtime.GOOS == "linux" && !strings.HasPrefix(device.SystemVersion, "Linux/") {
		t.Fatalf("Linux runtime claimed %q", device.SystemVersion)
	}
	if runtime.GOOS != "darwin" && strings.HasPrefix(device.SystemVersion, "macOS") {
		t.Fatalf("non-macOS runtime claimed %q", device.SystemVersion)
	}
}
