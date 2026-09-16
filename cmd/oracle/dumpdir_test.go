package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckCTPDumpDir 钉住「CTP 夹具只有一个家」。
//
// ⚠️ 这条测试的由来：在它之前，「只能落 testdata/ctp」只写在 flag 帮助文字里，
// 而 Fixture.Write 对目录一句校验都没有、直接 MkdirAll ——
// 照抄文档里的 `-dump testdata/ctp` 在 cmd/oracle 下跑，夹具会落进 cmd/oracle/testdata/ctp，
// 路径字面上完全正确，肉眼复核也看不出不对。
func TestCheckCTPDumpDir(t *testing.T) {
	root := filepath.FromSlash("C:/repo")
	for _, c := range []struct {
		name, dir string
		ok        bool
		wantWhy   string
	}{
		{"仓库根下的 testdata/ctp", "C:/repo/testdata/ctp", true, ""},
		{"大小写不同（Windows）", "c:/REPO/TestData/CTP", true, ""},
		{"末尾带斜杠", "C:/repo/testdata/ctp/", true, ""},
		{"嵌套模块下同名目录", "C:/repo/cmd/oracle/testdata/ctp", false, "嵌套模块"},
		{"天勤语料目录", "C:/repo/testdata/probes", false, "天勤"},
		{"再深一层子目录", "C:/repo/testdata/ctp/20260917", false, "不是仓库根下的"},
		{"仓库外", "C:/Temp/ctp", false, "不是仓库根下的"},
	} {
		err := checkCTPDumpDir(filepath.FromSlash(c.dir), root)
		if c.ok {
			if err != nil {
				t.Errorf("⚠️ %s 应当放行：%v", c.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("⚠️ %s 应当被拒 —— 不报错时夹具会静默落到错地方", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantWhy) {
			t.Errorf("⚠️ %s 的理由没说到点上：要提到 %q，得到 %v", c.name, c.wantWhy, err)
		}
	}
}

// TestCheckCTPDumpDirTellsApartTheTwoMistakes 断言两种落错给的是**不同**的理由。
//
// ⚠️ 一律回一句「目录不对」也能让上面那条通过（只要它恰好含得上），
// 而这两种落错的补救完全不同：一种是换目录，一种是「你以为对的那个字面路径不对」。
func TestCheckCTPDumpDirTellsApartTheTwoMistakes(t *testing.T) {
	root := filepath.FromSlash("C:/repo")
	if checkCTPDumpDir(filepath.FromSlash("C:/repo/cmd/oracle/testdata/ctp"), root) == nil ||
		checkCTPDumpDir(filepath.FromSlash("C:/repo/testdata/probes"), root) == nil {
		t.Fatal("前提：两种落错都要被拒")
	}
	// ⚠️ 比的是**理由**，不是整句错误：整句里带着各自的路径，
	// 路径不同整句就不同 —— 拿整句比，理由塌成一句也照样绿（破坏 632 起初就是这样漏掉的）。
	nested := dumpDirWhy(filepath.FromSlash("C:/repo/cmd/oracle/testdata/ctp"), root)
	probes := dumpDirWhy(filepath.FromSlash("C:/repo/testdata/probes"), root)
	if nested == probes {
		t.Errorf("⚠️ 两种落错给了同一句话 —— 它们的补救不一样：\n%v", nested)
	}
}

// TestRepoRootFindsThisRepo 断言 repoRoot 在本仓库里认得出根，且根下真有 testdata/ctp。
//
// ⚠️ 反向也要成立：若 repoRoot 一律返回当前目录（cmd/oracle），
// 上面那些纯函数照样全绿，而真跑起来会把 cmd/oracle/testdata/ctp 当成家。
func TestRepoRootFindsThisRepo(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "testdata", "ctp")); err != nil {
		t.Errorf("⚠️ 认出来的仓库根 %s 下没有 testdata/ctp —— 认错了根：%v", root, err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(filepath.Clean(root), filepath.Clean(cwd)) {
		t.Errorf("⚠️ 仓库根被认成了当前目录 %s —— 测试在 cmd/oracle 下跑，"+
			"根应当是它上面两层（认成当前目录时，落错到嵌套模块那一种就拦不住了）", cwd)
	}
}

// TestCTPDumpDirIsWiredIntoEveryCommand 是 -dump 守卫的**接线测试**。
//
// ⚠️ 为什么它必须存在：上面三条测的是判定，而判定全绿、忘了接线，
// 表现恰好是「一切正常」—— 夹具照样落盘、命令照样成功，只是落在没人读的目录里。
// 这个模块栽过一模一样的跟头两次（probe.decideFallback 抽成纯函数忘了接线、
// ctp.Client.Valve 忘了接线），所以这里从**命令入口**出发。
//
// ⚠️ 六个命令都要各自断言：接线是**每个命令一处**、不是共享一处，
// 少接一个不会让别的命令变红。
//
// ⚠️ `-env` 指向一个不存在的文件：守卫在 `fs.Parse` 之后、连柜台之前，
// 所以走到这一步本来就连不上；万一守卫被删掉，失败会停在「读不到凭据文件」——
// 那与本条要的错**不是同一句**，于是照样红，而且**不会真的碰柜台**。
func TestCTPDumpDirIsWiredIntoEveryCommand(t *testing.T) {
	const noEnv = "___no_such_env___"
	for _, c := range []struct {
		cmd  string
		run  func([]string) error
		args []string
	}{
		{"ctp-closefee", runCTPCloseFee, []string{"-symbol", "DCE.m2701", "-mode", "bare"}},
		{"ctp-closeorder", runCTPCloseOrder, []string{"-symbol", "DCE.m2701"}},
		{"ctp-slices", runCTPSlices, []string{"-symbol", "SHFE.rb2701"}},
		{"ctp-params", runCTPParams, nil},
		{"ctp-order", runCTPOrder, []string{"-symbol", "SHFE.rb2701"}},
		{"ctp-roundtrip", runCTPRoundTrip, []string{"-symbol", "SHFE.rb2701"}},
	} {
		// ⚠️ 用的正是最容易照抄错的那个路径：字面上就是 testdata/ctp，
		// 而在 cmd/oracle 下跑时它解析到 cmd/oracle/testdata/ctp。
		args := append([]string{"oracle", c.cmd, "-env", noEnv, "-dump", "testdata/ctp"}, c.args...)
		err := c.run(args)
		if err == nil {
			t.Errorf("⚠️ %s 收下了 -dump testdata/ctp —— 守卫没接到这个命令上", c.cmd)
			continue
		}
		if !strings.Contains(err.Error(), "CTP 夹具只有一个家") {
			t.Errorf("⚠️ %s 报的不是 -dump 那个错（守卫没接上，失败发生在后面）：%v", c.cmd, err)
		}
	}
	// 反向：正确的目录不该被这道守卫拦下 —— 一律报错也能让上面全绿。
	// ⚠️ 这里只走到守卫之后：紧接着的 LoadEnv 必然失败，所以断言「错的不是 -dump 那句」。
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	err = runCTPParams([]string{"oracle", "ctp-params", "-env", noEnv,
		"-dump", filepath.Join(root, "testdata", "ctp")})
	if err == nil {
		t.Fatal("前提：-env 指向不存在的文件，总该报错")
	}
	if strings.Contains(err.Error(), "CTP 夹具只有一个家") {
		t.Errorf("⚠️ 仓库根下的 testdata/ctp 反而被拦了 —— 守卫一律报错的话，上面六条也会全绿：%v", err)
	}
}
