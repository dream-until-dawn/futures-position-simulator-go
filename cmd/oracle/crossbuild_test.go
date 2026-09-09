package main

import (
	"os"
	"os/exec"
	"testing"
)

// TestCrossCompiles 钉住 roadmap 里那句「`GOOS=linux go build` 能过」。
//
// # ⚠️ 它为什么必须真的去编译
//
// 这句话在 roadmap 里当过**两次**假话：第一次是写下时就没验过
// （`eb74361` 标题就叫「上一句是假的」），⚠️ **而那次只改了 5 个 .go 文件、
// 一个 .md 都没碰** —— 于是「已验证能过」原封不动地留在文档里，
// 20260910 评审再次实测才抓到。
//
// > **一个标题就是「上一句是假的」的提交，把假话留在了文档里。**
//
// 一条**声称**能不能过编译的话，只有真去编一次才检查得了 ——
// 读源码、数 build tag 都只能查到「看起来对」。
//
// # ⚠️ 找不到 go 就失败，不跳过
//
// `t.Skip` 在这里正是本仓库反复栽的那个坑：一条跳过的守卫与一条通过的守卫
// 在 `go test ./...` 的汇总里长得一模一样（都打 `ok`）。
// 这条测试**只在 go 工具链存在时才有意义**，而它跑起来的环境里 go 必然存在
// —— 它就是被 go 跑起来的。所以「找不到 go」是环境坏了，该红。
func TestCrossCompiles(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatalf("⚠️ PATH 上找不到 go —— 这条守卫**没有查任何东西**。"+
			"它是被 go 跑起来的，所以找不到 go 意味着环境坏了：%v", err)
	}
	for _, goos := range []string{"linux", "darwin"} {
		// ⚠️ `-o` 指向临时目录：不给 `-o` 时单 main 包会把可执行文件
		// 写进工作树，而那是一次**测试改树**——本仓库两个会话共用一棵树。
		cmd := exec.Command("go", "build", "-o", t.TempDir(), "./...")
		cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("⚠️ GOOS=%s 编不过 —— roadmap 里「已验证能过」那句因此是**假话**。\n"+
				"⚠️ 多半是某个**与平台无关**的函数被放进了 `_windows.go`："+
				"该不该带 tag 看的是它用了什么，不是它的邻居是谁。\n%s", goos, out)
		}
	}
}
