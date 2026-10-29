package ctp

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// nativeHandshakeBindingError 是客户端本地绑定失败码，不是柜台业务错误码。
const nativeHandshakeBindingError int32 = -10001

// nativeHandshakeSlot 仅服务固定ABI的三种握手，不提供订单或任意方法调用入口。
func nativeHandshakeSlot(name string) (uintptr, error) {
	switch name {
	case "ReqAuthenticate":
		return 0x50, nil
	case "ReqUserLogin":
		return 0x68, nil
	case "ReqSettlementInfoConfirm":
		return 0xe0, nil
	default:
		return 0, fmt.Errorf("未登记的原生握手方法")
	}
}

// validateNativeHandshakeDLLs 必须在LoadDLL和CreateApi之前调用，禁止未知二进制套用固定虚表偏移。
func validateNativeHandshakeDLLs(dir string) error {
	return validateNativeHandshakeFiles(dir, runtime.GOARCH)
}

func validateNativeHandshakeFiles(dir, arch string) error {
	if arch != "amd64" {
		return fmt.Errorf("原生握手适配仅验收Windows amd64")
	}
	for _, file := range []struct{ name, sha256 string }{
		{"ctp_trade.dll", "18bf025ceb86b8886dcba4091bb4410ee4cad8ae851e707183159d5bd0245abc"},
		{"thosttraderapi_se.dll", "774ca2f3bba3009e72ba1f8a5e7a752d569bbbd999f19017e7cabc5c32c08bdc"},
	} {
		data, err := os.ReadFile(filepath.Join(dir, file.name))
		if err != nil {
			return fmt.Errorf("原生握手DLL不可读: %s", file.name)
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != file.sha256 {
			return fmt.Errorf("原生握手DLL未验收: %s", file.name)
		}
	}
	return nil
}
