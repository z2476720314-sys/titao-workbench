package psx

import (
	"fmt"
	"strings"
	"testing"
)

// TestEscapePathEscapesQuotes 守住「路径含双引号会提前结束 JSX 字符串字面量」。
// 旧实现只转义反斜杠，路径里的 " 会让整段 JSX 变成语法错误，
// PS 只回报一个含糊的 8800，极难定位。
func TestEscapePathEscapesQuotes(t *testing.T) {
	got := escapePath(`E:\a"b\c.png`)
	want := `E:\\a\"b\\c.png`
	if got != want {
		t.Errorf("escapePath = %q，期望 %q", got, want)
	}
}

// TestEscapePathStripsNewlines 守住换行不会把 JSX 语句截断成两行。
func TestEscapePathStripsNewlines(t *testing.T) {
	got := escapePath("E:\\a\r\nb.png")
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("escapePath 结果仍含换行: %q", got)
	}
}

// TestEscapePathKeepsNormalPath 守住正常路径只翻倍反斜杠，不破坏本身。
func TestEscapePathKeepsNormalPath(t *testing.T) {
	got := escapePath(`E:\套图模板\紫色IMD模版1.tif`)
	want := `E:\\套图模板\\紫色IMD模版1.tif`
	if got != want {
		t.Errorf("escapePath = %q，期望 %q", got, want)
	}
}

// TestCheckRenderLogClean 守住正常日志不误报。
func TestCheckRenderLogClean(t *testing.T) {
	log := "OPEN tpl.tif 800x4800\nSRC 背面.psb <- 插画/背面\nFILL 背面.psb (945x1890)\nFLAT t=0.85 -> flat_0.png"
	if err := checkRenderLog(log); err != nil {
		t.Errorf("正常日志被误判为失败: %v", err)
	}
}

// TestCheckRenderLogDetectsMiss 守住「有 PSB 没替换」必须报错。
// 否则整批会成功导出原模板图案，用户完全无感。
func TestCheckRenderLogDetectsMiss(t *testing.T) {
	log := "OPEN tpl.tif\nMISS 插画/背面 拷贝 3\nFILL 背面.psb\nFLAT t=0.85 -> flat_0.png"
	err := checkRenderLog(log)
	if err == nil {
		t.Fatal("MISS 行未被检出")
	}
	if !strings.Contains(err.Error(), "背面 拷贝 3") {
		t.Errorf("错误信息未指出具体图层: %v", err)
	}
}

// TestCheckRenderLogDetectsErr 守住「替换出错」必须报错。
func TestCheckRenderLogDetectsErr(t *testing.T) {
	log := "ERR 背面.psb 替换失败: 权限不足\nFLAT t=0.85 -> flat_0.png"
	err := checkRenderLog(log)
	if err == nil {
		t.Fatal("ERR 行未被检出")
	}
	if !strings.Contains(err.Error(), "权限不足") {
		t.Errorf("错误信息未带上原因: %v", err)
	}
}

// TestCheckRenderLogBothKinds 守住两类问题同时出现时都报出来。
func TestCheckRenderLogBothKinds(t *testing.T) {
	log := "MISS a\nMISS b\nERR c"
	err := checkRenderLog(log)
	if err == nil {
		t.Fatal("未检出问题")
	}
	msg := err.Error()
	for _, want := range []string{"2 个智能对象未被替换", "1 个替换出错", "a", "b", "c"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息缺少 %q: %s", want, msg)
		}
	}
}

// TestRenderJSXHonoursNoCrop 守住「禁止裁切」开关确实进了 JSX。
//
// 模板里的 NOCROP 分支会把 T>1 压回 s0（完整放下），若不传或传错字面量，
// 开关就成摆设——宽扁图案仍会被横向裁掉。
func TestRenderJSXHonoursNoCrop(t *testing.T) {
	for _, want := range []bool{true, false} {
		got := strings.ReplaceAll(jsxRender, "__NOCROP__", fmt.Sprintf("%t", want))
		if strings.Contains(got, "__NOCROP__") {
			t.Fatalf("NoCrop=%v：JSX 里仍残留 __NOCROP__ 占位符", want)
		}
		if !strings.Contains(got, fmt.Sprintf("var NOCROP = %t;", want)) {
			t.Errorf("NoCrop=%v：JSX 未正确写入 NOCROP 字面量", want)
		}
	}
}
