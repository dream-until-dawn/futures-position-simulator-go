package ctp

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNativeHandshakePinnedDLLs 用依赖内实际二进制核验固定ABI，未知文件和架构必须在加载前拒绝。
func TestNativeHandshakePinnedDLLs(t *testing.T) {
	dir, _, err := DLLDir()
	if err != nil {
		t.Fatal(err)
	}
	if err = validateNativeHandshakeFiles(dir, "amd64"); err != nil {
		t.Fatal("固定原生DLL被错误拒绝", err)
	}
	for _, arch := range []string{"arm64", "386", ""} {
		if validateNativeHandshakeFiles(dir, arch) == nil {
			t.Fatal("未知原生架构被放行")
		}
	}
	for _, bad := range []string{"ctp_trade.dll", "thosttraderapi_se.dll"} {
		local := t.TempDir()
		for _, name := range []string{"ctp_trade.dll", "thosttraderapi_se.dll"} {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if name == bad {
				data[len(data)-1] ^= 1
			}
			if err = os.WriteFile(filepath.Join(local, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if validateNativeHandshakeFiles(local, "amd64") == nil {
			t.Fatal("篡改原生DLL仍被放行", bad)
		}
	}
	if validateNativeHandshakeFiles(t.TempDir(), "amd64") == nil {
		t.Fatal("缺失原生DLL仍被放行")
	}
}

// TestNativeHandshakeMethodBoundary 不把握手修复变成任意虚表调用或订单入口。
func TestNativeHandshakeMethodBoundary(t *testing.T) {
	for name, want := range map[string]uintptr{"ReqAuthenticate": 0x50, "ReqUserLogin": 0x68, "ReqSettlementInfoConfirm": 0xe0} {
		got, err := nativeHandshakeSlot(name)
		if err != nil || got != want {
			t.Fatal("固定原生握手偏移错误", name)
		}
	}
	for _, name := range []string{"", "ReqOrderInsert", "ReqOrderAction", "ReqQryTradingAccount"} {
		if _, err := nativeHandshakeSlot(name); err == nil {
			t.Fatal("未登记原生方法被放行", name)
		}
	}
}
