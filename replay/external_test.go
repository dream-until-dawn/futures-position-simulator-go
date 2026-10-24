package replay

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestExternalModuleIntegration 在独立go.mod中编译和执行正反例，关闭代理/校验联网。
func TestExternalModuleIntegration(t *testing.T) {
	cmd := exec.Command("go", "test", ".", "-count=1", "-v")
	cmd.Dir = filepath.Join("..", "examples", "offline-replay")
	env := []string{}
	for _, v := range os.Environ() {
		if len(v) >= 7 && (v[:7] == "GOWORK=" || v[:7] == "GOPROXY") {
			continue
		}
		if len(v) >= 8 && v[:8] == "GOSUMDB=" {
			continue
		}
		env = append(env, v)
	}
	cmd.Env = append(env, "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("独立模块接入失败: %v\n%s", err, output)
	}
}
