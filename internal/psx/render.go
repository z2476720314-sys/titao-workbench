package psx

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 摆放方式：决定「基准缩放」怎么算。
//
// 实测结论（标尺实验）：模板的智能对象变换已经把 945x1890 的画布映射到壳面，
// 满格图案按画布 1:1 置入正好铺满。但画布 1:1 只对满格图案成立：
// 宽扁图案（如 945x574）在 1:1 时宽度已顶满壳面，再放大就横向裁掉设计元素。
// 因此 auto 统一采用 contain 语义（= 完整放下），保证图案整体可见。
const (
	FitAuto    = "auto"    // 完整放下为基准（默认，推荐；等价 contain）
	FitContain = "contain" // 以「完整放下」为基准
	FitCover   = "cover"   // 以「刚好铺满」为基准
	FitStretch = "stretch" // 拉伸变形
	FitNone    = "none"    // 原始像素，不做任何缩放
)

// RenderRequest 描述一次「按多档缩放渲染整幅拼合图」的请求。
type RenderRequest struct {
	// TemplateCopy 是模板的临时副本路径。原始模板永不被 PS 打开。
	TemplateCopy string
	// Pattern 是规范化后的图案路径（已去掉 dpi 元数据）。
	Pattern string
	// Group 是要替换的顶层图层组名，例如「插画」。
	Group string
	// Fit 是摆放方式，见 FitAuto 等常量。
	Fit string
	// Scales 是各档填充倍数：1.0 = 刚好铺满壳面，<1 留白，>1 超出被裁切。
	Scales []float64
	// OffsetX / OffsetY 占画布宽/高的比例；auto/contain 以贴底为基准，其余居中。
	OffsetX float64
	OffsetY float64
	// OutDir 是拼合图输出目录，脚本会写入 flat_0.png…flat_N.png。
	OutDir string
	// NoCrop 为 true 时把缩放硬性限制在「完整放下」以内（T 超过 1 会被压回 1），
	// 保证图案任何可见部分都不会被壳边裁掉。cover / stretch 不受影响。
	NoCrop bool
	// PSVersion 是 Photoshop 版本号，用于版本自适应能力校验。
	//
	// 留空时由 Render 从当前会话实时读取（每次渲染多一次 COM 属性读取），
	// 调用方若已缓存版本（cfg.PSVersion）可直接传入以省掉这次往返。
	PSVersion string
}

// FlatPath 返回第 i 档拼合图的路径。
func (r RenderRequest) FlatPath(i int) string {
	return fmt.Sprintf("%s/flat_%d.png", strings.ReplaceAll(r.OutDir, "\\", "/"), i)
}

// TemplateInfo 是模板的结构探查结果。
type TemplateInfo struct {
	Name      string      `json:"name"`
	Width     int         `json:"width"`
	Height    int         `json:"height"`
	CellSize  int         `json:"cellSize"`
	Rows      int         `json:"rows"`
	Guides    []float64   `json:"guides"`
	Groups    []string    `json:"groups"`
	Layers    []LayerInfo `json:"layers"`
	ArtGroups []ArtGroup  `json:"artGroups"`
}

// LayerInfo 是一个顶层图层。
type LayerInfo struct {
	Name     string  `json:"name"`
	Kind     string  `json:"kind"` // ArtLayer / LayerSet
	Visible  bool    `json:"visible"`
	Opacity  float64 `json:"opacity"`
	Blend    string  `json:"blend"`
	Children int     `json:"children"`
}

// ArtGroup 是一个可套图的图层组及其内部智能对象。
type ArtGroup struct {
	Group string   `json:"group"`
	PSBs  []string `json:"psbs"`  // 内部 PSB 源名（去重后）
	Count int      `json:"count"` // 智能对象总数（含共享同一 PSB 的格子）
}

const jsxProbeTemplate = `(function(){
  var out = { name: "", width: 0, height: 0, guides: [], layers: [], artGroups: [] };
  var doc = app.open(new File("__TPL__"));
  app.activeDocument = doc;
  out.name = doc.name;
  out.width = Math.round(doc.width.as("px"));
  out.height = Math.round(doc.height.as("px"));

  try {
    var gs = doc.guides;
    for (var i = 0; i < gs.length; i++) {
      var g = gs[i];
      if (String(g.direction) === "Direction.HORIZONTAL") {
        out.guides.push(Math.round(g.coordinate.as("px")));
      }
    }
  } catch (e) {}

  function walk(container, depth, arr) {
    for (var i = 0; i < container.layers.length; i++) {
      var L = container.layers[i];
      var row = {
        name: String(L.name),
        kind: (L.typename === "LayerSet") ? "LayerSet" : "ArtLayer",
        visible: false, opacity: 100, blend: "", children: 0
      };
      try { row.visible = L.visible; } catch (e) {}
      try { row.opacity = Math.round(L.opacity * 10) / 10; } catch (e) {}
      try { row.blend = String(L.blendMode); } catch (e) {}
      if (L.typename === "LayerSet") {
        try { row.children = L.layers.length; } catch (e) {}
      }
      arr.push(row);
    }
  }
  walk(doc, 0, out.layers);

  // 找每个图层组里的智能对象，按内部 PSB 名去重
  function collectSo(container, acc) {
    for (var i = 0; i < container.layers.length; i++) {
      var L = container.layers[i];
      if (L.typename === "LayerSet") { collectSo(L, acc); continue; }
      try {
        if (L.kind != LayerKind.SMARTOBJECT) continue;
      } catch (e) { continue; }
      var nm = "?";
      try {
        app.activeDocument = doc;
        doc.activeLayer = L;
        executeAction(stringIDToTypeID("placedLayerEditContents"), new ActionDescriptor(), DialogModes.NO);
        nm = String(app.activeDocument.name);
        app.activeDocument.close(SaveOptions.DONOTSAVECHANGES);
        app.activeDocument = doc;
      } catch (e) { nm = "ERR:" + e.message; }
      acc.total++;
      if (!acc.set[nm]) { acc.set[nm] = true; acc.names.push(nm); }
    }
  }

  for (var i = 0; i < doc.layers.length; i++) {
    var L = doc.layers[i];
    if (L.typename !== "LayerSet") continue;
    var acc = { set: {}, names: [], total: 0 };
    collectSo(L, acc);
    if (acc.total > 0) {
      out.artGroups.push({ group: String(L.name), psbs: acc.names, count: acc.total });
    }
  }

  doc.close(SaveOptions.DONOTSAVECHANGES);
  return JSON.stringify(out);
})()`

// ProbeTemplate 打开模板副本，读取尺寸、参考线、图层树与可套图的图层组。
//
// 模板会被复制到 tmpCopy 后打开，原文件不受影响。
func (s *Session) ProbeTemplate(templateCopy string) (*TemplateInfo, error) {
	jsx := strings.ReplaceAll(jsxProbeTemplate, "__TPL__", escapePath(templateCopy))

	var raw string
	err := s.Do(func(c *Conn) error {
		out, err := c.DoJavaScript(jsx)
		raw = out
		return err
	})
	if err != nil {
		return nil, err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("探查模板返回空结果")
	}
	if strings.HasPrefix(raw, "E:") || strings.HasPrefix(raw, "错误") {
		return nil, fmt.Errorf("探查模板失败: %s", raw)
	}

	var info TemplateInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		return nil, fmt.Errorf("解析探查结果: %w（原文前 200 字: %.200s）", err, raw)
	}
	// 分割线取模板自带的水平参考线；参考线就是每格的边界，间距即格高。
	// 没有参考线时退回默认 800。
	info.CellSize = 800
	if len(info.Guides) >= 2 {
		gap := info.Guides[1] - info.Guides[0]
		if gap >= 1 && gap < float64(info.Height) {
			info.CellSize = int(gap + 0.5)
		}
	}
	if info.CellSize > 0 {
		info.Rows = info.Height / info.CellSize
	}
	return &info, nil
}

// escapePath 把 Windows 路径转成可安全嵌入 JSX 双引号字符串字面量的形式。
//
// 必须同时转义反斜杠和双引号：路径里出现 `"` 会提前结束字符串，
// 后面整段 JSX 变成语法错误，PS 只会回报一个含糊的 8800。
func escapePath(p string) string {
	p = strings.ReplaceAll(p, `\`, `\\`)
	p = strings.ReplaceAll(p, `"`, `\"`)
	p = strings.ReplaceAll(p, "\r", "")
	p = strings.ReplaceAll(p, "\n", "")
	return p
}

const jsxRender = `(function(){
  var log = [];
  var doc = app.open(new File("__TPL__"));
  app.activeDocument = doc;
  log.push("OPEN " + doc.name + " " + doc.width.as("px") + "x" + doc.height.as("px"));

  var TARGET_GROUP = "__GROUP__";
  var scanRoot = null;
  for (var g = 0; g < doc.layers.length; g++) {
    if (doc.layers[g].typename == "LayerSet" && doc.layers[g].name == TARGET_GROUP) {
      scanRoot = doc.layers[g]; break;
    }
  }
  if (!scanRoot) { doc.close(SaveOptions.DONOTSAVECHANGES); return "NOGROUP " + TARGET_GROUP; }

  // 1) 收集智能对象，按内部 PSB 名分组（多格共享同一 PSB 时只替换一次）
  var byPsb = {}, order = [];
  function collect(container, path) {
    for (var i = 0; i < container.layers.length; i++) {
      var L = container.layers[i];
      if (L.typename == "LayerSet") { collect(L, path + L.name + "/"); continue; }
      try { if (L.kind != LayerKind.SMARTOBJECT) continue; } catch (e) { continue; }
      var nm = "?";
      try {
        app.activeDocument = doc;
        doc.activeLayer = L;
        executeAction(stringIDToTypeID("placedLayerEditContents"), new ActionDescriptor(), DialogModes.NO);
        nm = String(app.activeDocument.name);
        app.activeDocument.close(SaveOptions.DONOTSAVECHANGES);
        app.activeDocument = doc;
      } catch (e) { nm = "ERR:" + e.message; }
      if (!byPsb[nm]) { byPsb[nm] = []; order.push(nm); }
      byPsb[nm].push(path + L.name);
    }
  }
  collect(scanRoot, TARGET_GROUP + "/");
  for (var o = 0; o < order.length; o++) {
    log.push("SRC " + order[o] + " <- " + byPsb[order[o]].join(" | "));
  }

  // 2) 逐档缩放：每档替换一遍 PSB 并导出整幅拼合 PNG
  var patFile = new File("__PAT__");
  var TS = __SCALES__;
  for (var si = 0; si < TS.length; si++) {
    var T = TS[si];
    for (var s = 0; s < order.length; s++) {
      var psbName = order[s];
      if (psbName.indexOf("ERR:") === 0) { log.push("SKIP " + psbName); continue; }

      var entered = false, guard = 0;
      (function walk(container) {
        if (entered || guard++ > 5000) return;
        for (var i = 0; i < container.layers.length; i++) {
          var L = container.layers[i];
          if (L.typename == "LayerSet") { walk(L); if (entered) return; continue; }
          try { if (L.kind != LayerKind.SMARTOBJECT) continue; } catch (e) { continue; }
          app.activeDocument = doc;
          doc.activeLayer = L;
          try {
            executeAction(stringIDToTypeID("placedLayerEditContents"), new ActionDescriptor(), DialogModes.NO);
            var inner = app.activeDocument;
            if (String(inner.name) == psbName) {
              entered = true;
              for (var j = 0; j < inner.layers.length; j++) {
                try { inner.layers[j].visible = false; } catch (e) {}
              }
              var d = new ActionDescriptor();
              d.putPath(charIDToTypeID("null"), patFile);
              d.putEnumerated(charIDToTypeID("FTcs"), charIDToTypeID("QCSt"), charIDToTypeID("Qcsa"));
              executeAction(charIDToTypeID("Plc "), d, DialogModes.NO);

              var nl = inner.activeLayer;
              var cw = inner.width.as("px"), ch = inner.height.as("px");
              var b = nl.bounds;
              var pw = b[2].as("px") - b[0].as("px");
              var ph = b[3].as("px") - b[1].as("px");

              // 基准缩放：
              //   auto / contain = 「完整放下」s0（图案整体可见，至少一个方向顶到壳边）
              //   cover          = 「刚好铺满」s1（另一方向必然被裁）
              //
              // 曾经把 auto 定成「画布 1:1」，对满格图案等价，但对宽扁/窄长图案
              // 会让 1:1 就已经溢出壳面（例如 945x574 的图案 T=1 时宽度已顶满，
              // 再放大就横向裁掉设计元素）。改成 s0 后 auto 天然不裁切。
              var FIT = "__FIT__";
              var NOCROP = __NOCROP__;
              var s0 = Math.min(cw / pw, ch / ph);
              var s1 = Math.max(cw / pw, ch / ph);
              var sx, sy;
              if (FIT == "stretch")      { sx = cw / pw * T; sy = ch / ph * T; }
              else if (FIT == "none")    { sx = sy = T; }
              else if (FIT == "cover")   { sx = sy = s1 * T; }
              else                       { sx = sy = s0 * T; }   // auto / contain
              // 不裁切上限：T 超过 1 就必然有方向被壳边裁掉。
              if (NOCROP && FIT != "stretch" && FIT != "cover" && (sx > s0 || sy > s0)) {
                sx = sy = s0;
              }
              nl.resize(sx * 100, sy * 100, AnchorPosition.MIDDLECENTER);

              var b2 = nl.bounds;
              var OFX = __OFX__, OFY = __OFY__;
              var nw = b2[2].as("px") - b2[0].as("px"), nh = b2[3].as("px") - b2[1].as("px");
              var tx = (cw - nw) / 2 + cw * OFX;
              var ty = ((FIT == "auto" || FIT == "contain" || FIT == "") ? ch - nh : (ch - nh) / 2) + ch * OFY;
              if (NOCROP && FIT != "stretch" && FIT != "cover") {
                tx = Math.max(0, Math.min(cw - nw, tx));
                ty = Math.max(0, Math.min(ch - nh, ty));
              }
              nl.translate(
                tx - b2[0].as("px"),
                ty - b2[1].as("px")
              );
              if (si === 0) {
                log.push("  FIT " + FIT + " 图案 " + pw + "x" + ph + " 壳面 " + cw + "x" + ch +
                         " contain=" + s0.toFixed(3) + " cover=" + s1.toFixed(3));
              }
              inner.save();
              inner.close(SaveOptions.DONOTSAVECHANGES);
              if (si === 0) log.push("FILL " + psbName + " (" + cw + "x" + ch + ")");
              return;
            } else {
              inner.close(SaveOptions.DONOTSAVECHANGES);
            }
          } catch (e) { log.push("ERR " + psbName + " " + e.message); }
        }
      })(scanRoot);
      if (!entered) log.push("MISS " + psbName);
    }

    var dup = doc.duplicate("titao_flat");
    dup.flatten();
    dup.saveAs(new File("__OUTDIR__/flat_" + si + ".png"), new PNGSaveOptions(), true, Extension.LOWERCASE);
    dup.close(SaveOptions.DONOTSAVECHANGES);
    app.activeDocument = doc;
    log.push("FLAT t=" + T + " -> flat_" + si + ".png");
  }

  doc.close(SaveOptions.DONOTSAVECHANGES);
  return log.join("\n");
})()`

// Render 按各档缩放渲染整幅拼合图，返回脚本日志。
//
// 一次 PS 会话出全部候选，避免反复开关文档。
func (s *Session) Render(req RenderRequest) (string, error) {
	// 版本自适应前置校验：先把「这个 PS 到底能不能跑」问清楚，
	// 再决定要不要开模板。缺能力时立刻返回中文错误，好过脚本报 8800 或挂死。
	ver := req.PSVersion
	if strings.TrimSpace(ver) == "" {
		// 调用方没给版本：当场问一次 PS，比按未知版本盲跑更准确。
		if v, err := s.Probe(); err == nil {
			ver = v
		}
	}
	caps := For(ver)
	if err := caps.Check(); err != nil {
		return "", err
	}

	scales, err := json.Marshal(req.Scales)
	if err != nil {
		return "", fmt.Errorf("序列化档位: %w", err)
	}

	jsx := jsxRender
	jsx = strings.ReplaceAll(jsx, "__TPL__", escapePath(req.TemplateCopy))
	jsx = strings.ReplaceAll(jsx, "__PAT__", escapePath(req.Pattern))
	// 图层组名也嵌在 JSX 字符串字面量里，同样要转义
	jsx = strings.ReplaceAll(jsx, "__GROUP__", escapePath(req.Group))
	jsx = strings.ReplaceAll(jsx, "__FIT__", escapePath(req.Fit))
	jsx = strings.ReplaceAll(jsx, "__SCALES__", string(scales))
	jsx = strings.ReplaceAll(jsx, "__OFX__", fmt.Sprintf("%g", req.OffsetX))
	jsx = strings.ReplaceAll(jsx, "__OFY__", fmt.Sprintf("%g", req.OffsetY))
	jsx = strings.ReplaceAll(jsx, "__OUTDIR__", escapePath(req.OutDir))
	jsx = strings.ReplaceAll(jsx, "__NOCROP__", fmt.Sprintf("%t", req.NoCrop))

	var log string
	err = s.Do(func(c *Conn) error {
		out, err := c.DoJavaScript(jsx)
		log = out
		return err
	})
	if err != nil {
		return log, err
	}
	trimmed := strings.TrimSpace(log)
	if strings.HasPrefix(trimmed, "NOGROUP") {
		return log, fmt.Errorf("模板里找不到图层组「%s」", req.Group)
	}
	if err := checkRenderLog(trimmed); err != nil {
		return log, err
	}
	return log, nil
}

// checkRenderLog 检查 JSX 的日志里是否藏着替换失败。
//
// 为什么必须查：脚本内部对单个 PSB 的失败是 catch 住的，会记成 ERR/MISS 行
// 而不抛错。若不检查，整批会「成功」导出原模板图案，用户完全无感。
func checkRenderLog(log string) error {
	var miss, errs []string
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "MISS "):
			miss = append(miss, strings.TrimPrefix(line, "MISS "))
		case strings.HasPrefix(line, "ERR "):
			errs = append(errs, strings.TrimPrefix(line, "ERR "))
		}
	}
	if len(miss) == 0 && len(errs) == 0 {
		return nil
	}
	var parts []string
	if len(miss) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个智能对象未被替换（%s）", len(miss), strings.Join(miss, ",")))
	}
	if len(errs) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个替换出错（%s）", len(errs), strings.Join(errs, ",")))
	}
	return fmt.Errorf("Photoshop 渲染不完整：%s", strings.Join(parts, "、"))
}
