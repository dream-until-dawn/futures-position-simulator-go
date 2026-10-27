package futsim

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// offlineManualErrors 从实际验证器源码收集模块/模糊目标，手册不能再靠一组复制的固定数量长期自洽。
func offlineManualErrors(manual, source string) []string {
	modules := regexp.MustCompile(`\("([a-z]+)",\s*ROOT(?:\s*/\s*"[^"]+")?\)`).FindAllStringSubmatch(source, -1)
	fuzzTargets := regexp.MustCompile(`"(Fuzz[A-Za-z0-9_]+)"`).FindAllStringSubmatch(source, -1)
	var problems []string
	if len(modules) < 3 || len(fuzzTargets) < 5 {
		problems = append(problems, "验证源码配置不完整，不能确认手册同步")
	}
	for _, entry := range append(modules, fuzzTargets...) {
		target := entry[1]
		if !strings.Contains(manual, "`"+target+"`") {
			problems = append(problems, "手册缺少验证目标 "+target)
		}
	}
	return problems
}

func manualInputs(t *testing.T) (string, string) {
	t.Helper()
	manual, err := os.ReadFile("docs/development.md")
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("scripts/validate.py")
	if err != nil {
		t.Fatal(err)
	}
	return string(manual), string(source)
}

// TestOfflineManualMatchesValidator 核对当前手册实际列全统一入口，历史两模块/三目标记录不作为当前说明。
func TestOfflineManualMatchesValidator(t *testing.T) {
	manual, source := manualInputs(t)
	for _, problem := range offlineManualErrors(manual, source) {
		t.Error(problem)
	}
}

// TestOfflineManualRejectsMissingTargets 缺任意目标或空源码都必须被检出，不能对空清单报同步成功。
func TestOfflineManualRejectsMissingTargets(t *testing.T) {
	manual, source := manualInputs(t)
	for _, target := range []string{"root", "oracle", "external", "FuzzStateSequence", "FuzzDecodeBundle", "FuzzReadRestingReport", "FuzzRiskDeposit", "FuzzLifecycleRules"} {
		without := strings.ReplaceAll(manual, "`"+target+"`", "missing-target")
		if !strings.Contains(strings.Join(offlineManualErrors(without, source), "\n"), "手册缺少验证目标 "+target) {
			t.Fatalf("验证手册守卫未检出缺项 target=%s", target)
		}
	}
	if !strings.Contains(strings.Join(offlineManualErrors(manual, ""), "\n"), "源码配置不完整") {
		t.Fatal("验证手册守卫未识别空源码")
	}
}
