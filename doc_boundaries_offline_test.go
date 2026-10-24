package futsim

import (
	"fmt"
	"testing"
)

type dateCoverageFailure struct{}
type dateCoverageRecorder struct{ failed bool }

func (r *dateCoverageRecorder) Fatalf(f string, a ...any) {
	r.failed = true
	_ = fmt.Sprintf(f, a...)
	panic(dateCoverageFailure{})
}

// TestOfflineDateRoleLowerBound 覆盖0、阈值前一格和阈值，使用真实守卫而非复制比较式。
func TestOfflineDateRoleLowerBound(t *testing.T) {
	for _, n := range []int{0, 19, 20, 21} {
		r := &dateCoverageRecorder{}
		func() {
			defer func() {
				if p := recover(); p != nil {
					if _, ok := p.(dateCoverageFailure); !ok {
						panic(p)
					}
				}
			}()
			checkDateRoleCoverage(r, n)
		}()
		if r.failed != (n < 20) {
			t.Fatalf("日期角色下界未阻断或误拦截：n=%d failed=%v", n, r.failed)
		}
	}
}
