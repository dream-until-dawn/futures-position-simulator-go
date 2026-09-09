package ctp

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestSendIsTheOnlyPathToInsert 断言**报单接口只有一条到达路径，而它先过安全阀**。
//
// # ⚠️ 它查的是结构，不是行为
//
// 行为测试（「被拦的单不会发出去」）只能证明**这一次**接线是对的。
// 而这个模块栽过的正是「以后某一次」：`probe.decideFallback` 抽成纯函数、
// 7 条用例全绿、**忘了接线** —— 一条测试都没红，是破坏逼出来的。
//
//	验证   防这一次忘        —— 要有人想到写那条断言
//	结构   防以后任何一次忘  —— 不给第二条到达路径
//
// 所以这一条读**本包的源码**，核两件事：
//
//	① `ReqOrderInsert` 这个 proc 名在全包只出现一次
//	② 出现它的那个函数，在它之前调用了 c.Check
//
// ⚠️ 它在「有人新写了第二条路径」的那一刻就红 —— **不必等那条路径被用到**，
// 也不必有人想到去为它写用例。
func TestSendIsTheOnlyPathToInsert(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 5 {
		t.Fatalf("⚠️ 只扫到 %d 个源文件 —— 太少，本条在空转", len(files))
	}
	fn := regexp.MustCompile(`(?m)^func .*\{`)
	var hits []string
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		src := string(b)
		for _, proc := range []string{`"ReqOrderInsert"`} {
			idx := 0
			for {
				i := strings.Index(src[idx:], proc)
				if i < 0 {
					break
				}
				at := idx + i
				// 找它所在的函数：最后一个在它之前的 func 头。
				start := 0
				for _, loc := range fn.FindAllStringIndex(src[:at], -1) {
					start = loc[0]
				}
				body := src[start:at]
				hits = append(hits, f)
				if !strings.Contains(body, "c.Check(") {
					t.Errorf("⚠️ %s 里调用 %s 的那个函数**没有先过安全阀** —— "+
						"⚠️ 这不是「忘了写测试」，是**多了一条能发出委托而不经过阀的路径**。"+
						"函数头开始的片段：%.120s", f, proc, strings.TrimSpace(body))
				}
				idx = at + len(proc)
			}
		}
	}
	if checked < 3 {
		t.Fatalf("⚠️ 只读了 %d 个非测试源文件 —— 太少，本条在空转", checked)
	}
	// ⚠️ 两个方向都要卡：
	//   多于一处 → 有了第二条路径
	//   零处     → 报单能力没了，而那时上面的循环一次都不执行、本条恒绿
	switch len(hits) {
	case 1:
		t.Logf("ReqOrderInsert 只在 %s 出现一次，且该函数先过安全阀（扫了 %d 个源文件）",
			hits[0], checked)
	case 0:
		t.Fatal("⚠️ 全包找不到 ReqOrderInsert —— 报单路径不见了。" +
			"⚠️ 而那会让上面的循环一次都不执行，**本条因此恒绿**：" +
			"「没有第二条路径」与「一条路径都没有」在断言上长得一样")
	default:
		t.Errorf("⚠️ ReqOrderInsert 出现在 %d 处：%v —— "+
			"**只许有一条到达柜台报单接口的路径**。多一条就多一个可能绕过阀的地方，"+
			"而绕过它的那一次不会报错，只会发出一笔本不该发的单", len(hits), hits)
	}
}

// stripComments 去掉 `//` 注释，只留代码。
//
// ⚠️ 这是本仓库第 6 处「讲/用」判据，而**解法又是新的**：
// 前五处分别是 剥引号 / 邻近关键词 / 作用域 / 改文案绕开 / 删掉那句话。
// 这一处的理由最直接 —— **这条守卫的对象是「代码里有没有这种开关」，
// 那它就只该看代码**。在注释里写「我们不提供 sendUnchecked」是完全正当的，
// 而上一版会把那句话本身报成违规（它当场抓到了写它的人）。
//
// ⚠️ 它不完美：字符串字面量里的同名词仍会被算成代码。
// 但那一类在这里是**该报**的 —— 一个以那个名字调用的 proc 正是要防的东西。
func stripComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if k := strings.Index(line, "//"); k >= 0 {
			line = line[:k]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// TestNoBypassKnob 断言**没有任何「绕过安全阀」的开关**。
//
// ⚠️ 一个「紧急情况下跳过检查」的参数，会在紧急情况下**正好**被用到 ——
// 而紧急情况正是判断力最差的时候。所以这里禁的是那种开关的**存在**，
// 不是它的使用。
func TestNoBypassKnob(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	banned := []string{"sendUnchecked", "SendUnchecked", "skipCheck", "SkipCheck", "ForceSend"}
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		n++
		for _, w := range banned {
			if strings.Contains(stripComments(string(b)), w) {
				t.Errorf("⚠️ %s 里出现 %q —— **不许有绕过安全阀的开关**。"+
					"它会在紧急情况下正好被用到，而那是判断力最差的时候", f, w)
			}
		}
	}
	if n < 3 {
		t.Fatalf("⚠️ 只扫了 %d 个源文件 —— 太少，本条在空转", n)
	}
}
