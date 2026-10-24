package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

type greenAuditEntry struct {
	ID           int    `json:"id"`
	Category     string `json:"category"`
	Action       string `json:"action"`
	Status       string `json:"status"`
	EvidenceTest string `json:"evidence_test"`
}

// checkGreenAudit 保证审计不能遗漏、重复或把未补测试的条目声称已关闭。
func checkGreenAudit(breaks []Break, entries []greenAuditEntry) error {
	byID := map[int]Break{}
	for _, b := range breaks {
		id, err := strconv.Atoi(strings.Fields(b.Name)[0])
		if err != nil {
			return err
		}
		byID[id] = b
	}
	seen := map[int]bool{}
	for _, entry := range entries {
		b, ok := byID[entry.ID]
		if !ok || seen[entry.ID] || entry.Action == "" {
			return fmt.Errorf("审计编号不存在、重复或缺少措施：%d", entry.ID)
		}
		seen[entry.ID] = true
		switch entry.Category {
		case "requires_market_evidence", "intentional_demonstration", "offline_guard_gap":
		default:
			return fmt.Errorf("未知审计分类")
		}
		if entry.Status == "closed_offline" {
			if b.Expect != "red" || entry.EvidenceTest == "" || !testNamesOf(b)[entry.EvidenceTest] {
				return fmt.Errorf("离线关闭缺少红验证：%d", entry.ID)
			}
		} else if entry.Status != "retained_with_action" || b.Expect != "green" {
			return fmt.Errorf("审计状态与清单不符：%d", entry.ID)
		}
	}
	for id, b := range byID {
		if b.Expect == "green" && !seen[id] {
			return fmt.Errorf("预期绿未审计：%d", id)
		}
	}
	return nil
}

func TestGreenAuditCoversEveryDeclaredGap(t *testing.T) {
	var breaks []Break
	if err := json.Unmarshal(registryJSON, &breaks); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../docs/validation/green-audit-20261024.json")
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Entries []greenAuditEntry `json:"entries"`
	}
	if err = json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 38 {
		t.Fatal("原38项审计有遗漏")
	}
	if err = checkGreenAudit(breaks, report.Entries); err != nil {
		t.Fatal(err)
	}
	base := []Break{{Name: "1 gap", Expect: "green", Test: "TestA"}}
	good := greenAuditEntry{ID: 1, Category: "offline_guard_gap", Action: "补合成输入", Status: "retained_with_action"}
	if err = checkGreenAudit(base, []greenAuditEntry{good}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]greenAuditEntry{nil, {good, good}, {{ID: 1, Category: "invalid", Action: "x", Status: "retained_with_action"}}, {{ID: 1, Category: "offline_guard_gap", Action: "x", Status: "closed_offline", EvidenceTest: "TestA"}}} {
		if checkGreenAudit(base, bad) == nil {
			t.Fatal("损坏审计仍通过")
		}
	}
}
