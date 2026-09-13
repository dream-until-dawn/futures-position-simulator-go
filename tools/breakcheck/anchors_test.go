package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryBreakAnchorStillExists 钉住**清单里每一处锚点在当前代码里恰好出现一次**——
// 在 `go test` 里查，而不是等一轮全量破坏跑完才知道。
//
// # ⚠️ 它是被「同一种事故第四次发生」逼出来的
//
// 重构改了被锚定的代码、而清单没跟着改 ⇒ 那条破坏在全量里报「零层未成立」。
// 此前的四次：256/257/258/384、397/402、406，以及 20260913 的 407/412/415
// （把 `dumpSlices` 的中文注记换成阶段枚举，三条锚点里还写着旧注记）。
//
// 方法论 89 说的是「碰了被锚定的代码就重跑全量」—— 那是一条**纪律**，
// 而一轮全量要跑近一个小时，于是它只在送审前跑；这之间提交的每一次重构
// 都带着没被发现的孤儿破坏。**而锚点在不在，根本不需要跑破坏就能查**：
// 读文件、数次数，与 run() 里零层那一道是同一个判断（共用 anchorProblem）。
//
// # ⚠️ 它查不到的
//
// 锚点**还在、但已经挪进了死代码**（或被复制到别处而旧处删了）—— 次数仍是 1，
// 本条通过，而那条破坏改的是一段没人执行的代码。那一格仍然只有全量能看出来
// （它会表现为「期望红而仍然绿」）。⇒ 本条**不替代**方法论 89，只把最常见的那一种提前。
func TestEveryBreakAnchorStillExists(t *testing.T) {
	// ⚠️ 先证明判断本身有判别力：零次、两次、CRLF 转换后恰好一次，三种都要判对。
	// 否则一个恒返回空串的 anchorProblem，会让下面整份清单全绿。
	for _, c := range []struct {
		name, content, old string
		ok                 bool
	}{
		{"恰好一次", "a\nX\nb\n", "X\n", true},
		{"零次", "a\nb\n", "X\n", false},
		{"两次", "X\nX\n", "X\n", false},
		{"通篇 CRLF、锚点写 LF ⇒ 转换后恰好一次", "a\r\nX\r\nb\r\n", "X\nb", true},
		{"换行混着 ⇒ 不猜", "a\r\nX\nb\r\n", "a\nX", false},
	} {
		got := anchorProblem(c.content, AlsoEdit{File: "synthetic.go", Old: c.old}) == ""
		if got != c.ok {
			t.Fatalf("⚠️ anchorProblem 在「%s」上判成 ok=%v，要 %v —— **判断本身坏了，下面扫清单的结果不可信**",
				c.name, got, c.ok)
		}
	}

	// ⚠️ 再证明**扫描循环**会报：喂一份刻意坏掉的合成清单（一条锚点零次、一条 Also 两次、
	// 一条空操作），三处都要被点名。否则一个把报告那一支静默掉的循环，
	// 在当前（干净的）清单上与一个成立的循环长得一模一样。
	synth := []Break{
		{Name: "s1 零次", File: "f.go", Old: "gone", New: "x"},
		{Name: "s2 Also 两次", File: "f.go", Old: "keep", New: "x",
			Also: []AlsoEdit{{File: "g.go", Old: "dup", New: "x"}}},
		{Name: "s3 空操作", File: "f.go", Old: "noop", New: "noop"},
	}
	synthFiles := map[string]string{"f.go": "keep\nnoop\n", "g.go": "dup dup"}
	probs, n := orphanAnchors(synth, func(f string) (string, error) { return synthFiles[f], nil })
	if n != 4 || len(probs) != 3 ||
		!strings.HasPrefix(probs[0], "s1") || !strings.HasPrefix(probs[1], "s2") || !strings.HasPrefix(probs[2], "s3") {
		t.Fatalf("⚠️ 合成清单（3 处坏、4 处锚点）上报了 %d 处、数了 %d 处锚点：%v —— "+
			"**扫描循环坏了，下面扫真清单的结果不可信**", len(probs), n, probs)
	}

	var breaks []Break
	if err := json.Unmarshal(registryJSON, &breaks); err != nil {
		t.Fatalf("⚠️ 读不了破坏清单：%v", err)
	}
	files := map[string]bool{}
	probs, checked := orphanAnchors(breaks, func(f string) (string, error) {
		files[f] = true
		raw, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(f)))
		return string(raw), err
	})
	for _, p := range probs {
		t.Errorf("⚠️ %s —— 这条破坏在全量里会报「零层未成立」，即它**已经不在守任何东西**。"+
			"多半是被锚定的代码重构过而清单没跟着改", p)
	}
	// ⚠️ 反空转：比对的改动数不能少于破坏条数（每条至少一处）。
	if checked < len(breaks) || len(breaks) == 0 {
		t.Fatalf("⚠️ 只比对了 %d 处锚点，而清单有 %d 条破坏 —— 有破坏被跳过了", checked, len(breaks))
	}
	t.Logf("ⓘ %d 条破坏、%d 处锚点、%d 个文件，逐一数过", len(breaks), checked, len(files))
}

// orphanAnchors 逐条逐处查锚点，返回问题列表与**数过的锚点处数**。
//
// ⚠️ read 由调用方给：真清单读磁盘，自检喂合成内容 —— 同一个循环，两种输入。
func orphanAnchors(breaks []Break, read func(file string) (string, error)) (probs []string, checked int) {
	cache := map[string]string{}
	for _, b := range breaks {
		for _, e := range append([]AlsoEdit{{File: b.File, Old: b.Old, New: b.New}}, b.Also...) {
			content, ok := cache[e.File]
			if !ok {
				var err error
				if content, err = read(e.File); err != nil {
					probs = append(probs, b.Name+"："+e.File+" 读不到："+err.Error())
					continue
				}
				cache[e.File] = content
			}
			checked++
			if p := anchorProblem(content, e); p != "" {
				probs = append(probs, b.Name+"："+p)
			}
			// ⚠️ 锚点与替换相同 ⇒ 破坏「发生了」但什么都没改，次数检查拦不住这一种。
			if strings.TrimSpace(e.Old) == strings.TrimSpace(e.New) {
				probs = append(probs, b.Name+"："+e.File+" 上的 old 与 new 相同 —— 破坏是空操作")
			}
		}
	}
	return probs, checked
}
