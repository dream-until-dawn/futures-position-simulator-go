package ctp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoctpModuleMatchesGoMod 把 DLL 目录名里的版本号与 go.mod 钉在一起。
//
// ⚠️ 这是一处**必然会漂**的重复：升级 goctp 时 go.mod 变了，
// 而 `GoctpModule` 这个常量不会跟着变。漂开的表现是 DLL 找不到 ——
// 那反倒是好的（当场就知道），但**在换机器之前谁也不会跑到那一步**：
// 本机的模块缓存里旧版本还在，一切照常。
//
// 于是这条守卫让它在**提交前**就红。
func TestGoctpModuleMatchesGoMod(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.SplitN(GoctpModule, "@", 2)
	if len(want) != 2 {
		t.Fatalf("GoctpModule = %q，应当形如 path@vX.Y.Z", GoctpModule)
	}
	path, ver := want[0], want[1]
	var found string
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(strings.TrimSpace(l))
		if len(f) >= 2 && f[0] == path {
			found = f[1]
			break
		}
	}
	switch {
	case found == "":
		// ⚠️ 这一支是**预期内的**，直到 P2 写下第一行 ctpdefine 的引用为止：
		// 依赖是随第一行 import 落地的，不是随裁决落地的（ctp-oracle.md 第 1 节）。
		t.Skipf("⚠️ go.mod 里还没有 %s —— 这条守卫**什么都没查**。"+
			"它会在第一个 import ctpdefine 的文件出现之后开始生效", path)
	case found != ver:
		t.Errorf("⚠️ go.mod 里是 %s@%s，而 ctp.GoctpModule 写的是 %s —— "+
			"两处漂开了。DLL 会去一个**旧版本**的目录里找，"+
			"而本机模块缓存里那个旧版本多半还在，于是**在换机器之前不会有人发现**",
			path, found, GoctpModule)
	}
}

// TestDLLDirPrefersExplicitEnv 钉住三条路的**优先级**与「报告用了哪条」。
//
// ⚠️ 「找到了」与「从哪找到的」是两件事：一台机器上恰好能跑，
// 不等于换台机器也能跑。只报「找到了」会让那个差别在换机那天才出现。
func TestDLLDirPrefersExplicitEnv(t *testing.T) {
	t.Setenv(DLLDirEnv, filepath.FromSlash("/tmp/explicit"))
	t.Setenv("GOMODCACHE", filepath.FromSlash("/tmp/cache"))
	dir, how, err := DLLDir()
	if err != nil {
		t.Fatal(err)
	}
	if how != DLLDirEnv {
		t.Errorf("⚠️ 显式指定的 %s 没有优先 —— 用的是 %q", DLLDirEnv, how)
	}
	if dir != filepath.FromSlash("/tmp/explicit") {
		t.Errorf("⚠️ 显式路径被改写了：%q", dir)
	}

	os.Unsetenv(DLLDirEnv)
	dir, how, err = DLLDir()
	if err != nil {
		t.Fatal(err)
	}
	if how != "GOMODCACHE" {
		t.Errorf("⚠️ 没有落到 GOMODCACHE 这一条 —— 用的是 %q", how)
	}
	if !strings.Contains(filepath.ToSlash(dir), "goctp@") || !strings.HasSuffix(dir, "win") {
		t.Errorf("⚠️ 从模块缓存拼出来的路径不像 goctp 的 win 目录：%q", dir)
	}
	// ⚠️ 版本号必须出现在路径里：拼错版本会去一个不存在的目录，
	// 而那与「模块没下载」在错误信息上长得一样。
	if !strings.Contains(dir, strings.SplitN(GoctpModule, "@", 2)[1]) {
		t.Errorf("⚠️ 路径里没有版本号：%q", dir)
	}
}

// TestCheckDLLsNamesEveryMissingFile 钉住「逐个列」而不是「只查一个」。
//
// ⚠️ 只查 ctp_trade.dll 的话，缺 thosttraderapi_se.dll 时 LoadDLL 会**成功**，
// 而在第一次调用时才崩 —— 那时的错误与「参数写错了」长得一样。
func TestCheckDLLsNamesEveryMissingFile(t *testing.T) {
	dir := t.TempDir()
	err := CheckDLLs(dir)
	if err == nil {
		t.Fatal("⚠️ 空目录里 CheckDLLs 居然通过了")
	}
	for _, n := range RequiredDLLs {
		if !strings.Contains(err.Error(), n) {
			t.Errorf("⚠️ 错误里没点名缺了 %s —— 只说「缺文件」的话，"+
				"人会去补第一个然后再撞一次", n)
		}
	}
	// 只放一个进去，另一个仍要被点名。
	if err := os.WriteFile(filepath.Join(dir, RequiredDLLs[0]), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = CheckDLLs(dir)
	if err == nil {
		t.Fatalf("⚠️ 只有 %s 一个文件，CheckDLLs 却通过了 —— "+
			"那正是「LoadDLL 成功、第一次调用才崩」的那条路", RequiredDLLs[0])
	}
	if strings.Contains(err.Error(), RequiredDLLs[0]) {
		t.Errorf("⚠️ %s 已经在了，却还被列为缺失：%v", RequiredDLLs[0], err)
	}
}
