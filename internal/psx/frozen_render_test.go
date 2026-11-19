package psx

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func frozenTestCanvas(t *testing.T) *PrintCanvas {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 945, 1890))
	for y := 80; y < 980; y++ {
		for x := 35; x < 435; x++ {
			im.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	path := filepath.Join(t.TempDir(), "canvas.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return &PrintCanvas{Path: path, SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), SourceSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("source"))), Width: 945, Height: 1890, Placement: AppliedPlacement{Scale: .75, OffsetX: -.12, OffsetY: .08}, Verified: true}
}

func TestFrozenPhotoshopUsesIdenticalCanvasForEveryCandidate(t *testing.T) {
	c := frozenTestCanvas(t)
	req := RenderRequest{Canvas: c, Scales: []float64{.5, 2}, OutDir: t.TempDir(), Fit: FitCover, OffsetX: .4, OffsetY: -.4, AnchorX: 2}
	if _, err := frozenRenderJSX(req); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range req.Scales {
		got, err := os.ReadFile(req.PrintCanvasPath(i))
		if err != nil || string(got) != string(source) {
			t.Fatalf("candidate %d changed canonical pixels: %v", i, err)
		}
	}
}

type frozenScriptResult struct {
	Log                                                 string
	Bounds                                              [4]float64
	Files                                               map[string]string
	Saves, Places, OuterCloses, InnerCloses, FlatCloses int
	OuterChanged                                        bool
}

func executeFrozenScript(t *testing.T, req RenderRequest, width, height int, setup ...string) frozenScriptResult {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable for ExtendScript geometry simulation")
	}
	js, err := frozenRenderJSX(req)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "-")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("var width=%d,height=%d;\n", width, height) + frozenPhotoshopHarness + "\n" + strings.Join(setup, "\n") + "\nvar log=" + js + ";\nconsole.log(JSON.stringify({Log:log,Bounds:placed.bounds.map(function(x){return x.as('px');}),Files:files,Saves:saves,Places:places,OuterCloses:outerCloses,InnerCloses:innerCloses,FlatCloses:flatCloses,OuterChanged:outerChanged}));")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("execute JSX: %v\n%s", err, out)
	}
	var result frozenScriptResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("JSX output: %v\n%s", err, out)
	}
	return result
}

func TestFrozenPhotoshopSeparatesDistinctSourcesWithSameName(t *testing.T) {
	req := RenderRequest{Canvas: frozenTestCanvas(t), Scales: []float64{1}, OutDir: t.TempDir(), Group: "art"}
	result := executeFrozenScript(t, req, 945, 1890, "doc.layers[0].layers[1].source='/photoshop-temp/independent/shared.psb';")
	if err := checkRenderLog(result.Log); err != nil {
		t.Fatal(err)
	}
	if result.Places != 2 || result.Saves != 2 {
		t.Fatalf("independent source with same basename skipped: %+v", result)
	}
}

func TestFrozenPhotoshopUsesPixelsWithNonPixelRulerPreference(t *testing.T) {
	req := RenderRequest{Canvas: frozenTestCanvas(t), Scales: []float64{1}, OutDir: t.TempDir(), Group: "art"}
	result := executeFrozenScript(t, req, 945, 1890, "rulerScale=72;")
	if err := checkRenderLog(result.Log); err != nil {
		t.Fatal(err)
	}
	want := [4]float64{35, 80, 435, 980}
	for i := range want {
		if math.Abs(result.Bounds[i]-want[i]) > 1e-8 {
			t.Fatalf("ruler preference changed canonical origin: got%v want%v", result.Bounds, want)
		}
	}
}

func TestFrozenPhotoshopRefusesLinkedSourcesBeforeModification(t *testing.T) {
	req := RenderRequest{Canvas: frozenTestCanvas(t), Scales: []float64{1}, OutDir: t.TempDir(), Group: "art"}
	result := executeFrozenScript(t, req, 945, 1890, "doc.layers[0].layers[0].linked=true;")
	if err := checkRenderLog(result.Log); err == nil || !strings.Contains(err.Error(), "linked") {
		t.Fatalf("linked source accepted: %+v", result)
	}
	if result.Places != 0 || result.Saves != 0 {
		t.Fatalf("linked source modified: %+v", result)
	}
}

func TestFrozenPhotoshopPreservesAlreadyOpenDocuments(t *testing.T) {
	req := RenderRequest{Canvas: frozenTestCanvas(t), Scales: []float64{1}, OutDir: t.TempDir(), Group: "art"}
	for _, setup := range []string{"app.documents=[innerDoc];", "app.documents=[doc];"} {
		result := executeFrozenScript(t, req, 945, 1890, setup)
		if err := checkRenderLog(result.Log); err == nil || !strings.Contains(err.Error(), "already open") {
			t.Fatalf("preexisting document accepted: %+v", result)
		}
		if result.Saves != 0 || result.Places != 0 || result.InnerCloses != 0 || (setup == "app.documents=[doc];" && result.OuterCloses != 0) {
			t.Fatalf("preexisting document modified or closed: %+v", result)
		}
	}
}

func TestFrozenPhotoshopPreservesCanonicalMarginsAndSharedCells(t *testing.T) {
	c := frozenTestCanvas(t)
	for _, size := range [][2]int{{945, 1890}, {946, 1890}, {1890, 3780}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			req := RenderRequest{Canvas: c, Scales: []float64{.5, 2}, NoCrop: true, OutDir: t.TempDir(), CellSize: 800, Group: "art", Fit: FitStretch, OffsetX: .9, AnchorX: 2}
			result := executeFrozenScript(t, req, size[0], size[1])
			if err := checkRenderLog(result.Log); err != nil {
				t.Fatal(err)
			}
			mapScale := float64(size[1]) / 1890
			want := [4]float64{35 * mapScale, 80 * mapScale, 435 * mapScale, 980 * mapScale}
			for i := range want {
				if math.Abs(result.Bounds[i]-want[i]) > 1e-8 {
					t.Fatalf("canonical alpha margins changed: got %v want %v", result.Bounds, want)
				}
			}
			if result.Places != 2 || result.Saves != 2 || result.OuterChanged || result.OuterCloses != 1 || result.InnerCloses != 4 || result.FlatCloses != 2 {
				t.Fatalf("shared source or document ownership changed: %+v", result)
			}
			for i := range req.Scales {
				var meta RenderMetadata
				if err := json.Unmarshal([]byte(result.Files[req.MetadataPath(i)]), &meta); err != nil {
					t.Fatalf("missing/invalid receipt: %v; log=%s", err, result.Log)
				}
				if !meta.CanvasVerified || meta.FrameVerified || meta.CanvasSHA256 != c.SHA256 || meta.CanvasWidth != 945 || meta.CanvasHeight != 1890 || len(meta.Placements) != 2 || len(meta.StaticCells) != 1 || meta.StaticCells[0] != 1 {
					t.Fatalf("incomplete receipt: %+v", meta)
				}
				for pi, cell := range []int{0, 2} {
					want := c.Placement
					want.Cell = cell
					if meta.Placements[pi] != want {
						t.Fatalf("cell %d changed canonical placement: got %+v want %+v", cell, meta.Placements[pi], want)
					}
				}
			}
		})
	}
}

func TestFrozenPhotoshopRejectsUnknownMappingAndClosesOwnedDocuments(t *testing.T) {
	req := RenderRequest{Canvas: frozenTestCanvas(t), Scales: []float64{1}, OutDir: t.TempDir(), Group: "art"}
	result := executeFrozenScript(t, req, 945, 1800)
	if err := checkRenderLog(result.Log); err == nil || !strings.Contains(err.Error(), "unsupported canonical canvas mapping") {
		t.Fatalf("unknown mapping accepted: %s", result.Log)
	}
	if result.Places != 0 || result.Saves != 0 || len(result.Files) != 0 || result.OuterCloses != 1 || result.InnerCloses != 3 {
		t.Fatalf("unknown mapping modified or leaked documents: %+v", result)
	}
}

const frozenPhotoshopHarness = `
var files={}, saves=0, places=0, outerCloses=0, innerCloses=0, flatCloses=0, outerChanged=false,rulerScale=1;
function unit(n){return {as:function(){return n;}};}
function UnitValue(n,unit){if(unit!='px')throw Error('expected pixels');this.as=function(){return n;};}
var placed={bounds:[unit(0),unit(0),unit(0),unit(0)],resize:function(x,y){
  var b=this.bounds.map(function(x){return x.as('px');});
  var mx=(b[0]+b[2])/2,my=(b[1]+b[3])/2,w=(b[2]-b[0])*x/100,h=(b[3]-b[1])*y/100;
  this.bounds=[unit(mx-w/2),unit(my-h/2),unit(mx+w/2),unit(my+h/2)];
},translate:function(x,y){x=typeof x=='number'?x*rulerScale:x.as('px');y=typeof y=='number'?y*rulerScale:y.as('px');this.bounds=this.bounds.map(function(n,i){return unit(n.as('px')+(i%2?y:x));});}};
var innerDoc={id:2,name:'shared.psb',width:unit(width),height:unit(height),layers:[{visible:true}],activeLayer:placed,
 save:function(){saves++;},close:function(){innerCloses++;app.activeDocument=doc;}};
function layer(top){return {name:'art',source:'/photoshop-temp/shared.psb',visible:true,typename:'ArtLayer',kind:1,bounds:[unit(100),unit(top+100),unit(700),unit(top+700)]};}
var doc={id:1,name:'copy.tif',width:unit(800),height:unit(2400),layers:[{name:'art',visible:true,typename:'LayerSet',layers:[layer(0),layer(1600)]}],
 close:function(){outerCloses++;},duplicate:function(){return {flatten:function(){},saveAs:function(){},close:function(){flatCloses++;}};}};
var app={activeDocument:null,documents:[],open:function(){return doc;}};
function File(path){this.path=path;this.open=function(){return true;};this.write=function(data){files[this.path]=data;return true;};this.close=function(){return true;};}
function ActionDescriptor(){this.putPath=function(k,f){this.path=f.path;};this.putEnumerated=function(){};}
function ActionReference(){this.putEnumerated=function(){};}
function executeActionGet(){return {getObjectValue:function(){return {hasKey:function(){return true;},getBoolean:function(){return !!doc.activeLayer.linked;}};}};}
function stringIDToTypeID(x){return x;}
function charIDToTypeID(x){return x;}
function executeAction(action,d){
 if(action=='placedLayerEditContents') {innerDoc.fullName={fsName:doc.activeLayer.source};app.activeDocument=innerDoc;}
 else if(action=='Plc '){
  places++;if(app.activeDocument!==innerDoc)outerChanged=true;
  if(d.path.indexOf('print_canvas_')<0)throw Error('placed original pattern');
  // Photoshop native Place centered and scaled the visible 400x900 alpha box.
  placed.bounds=[unit(200),unit(300),unit(400),unit(750)];
 } else throw Error('unexpected action '+action);
}
var LayerKind={SMARTOBJECT:1},DialogModes={NO:0},SaveOptions={DONOTSAVECHANGES:0},AnchorPosition={MIDDLECENTER:0},Extension={LOWERCASE:0};
function PNGSaveOptions(){}
`

func TestPhotoshopVerifiedFrozenNoCropReachesSession(t *testing.T) {
	c := frozenTestCanvas(t)
	s := &Session{closed: true}
	if _, err := s.Render(RenderRequest{Canvas: c, NoCrop: true, PSVersion: "27.7.0", Scales: []float64{1}, OutDir: t.TempDir()}); !errors.Is(err, ErrClosed) {
		t.Fatalf("verified frozen canvas rejected before session: %v", err)
	}
}

func TestPhotoshopRejectsInvalidFrozenEvidenceBeforeCOM(t *testing.T) {
	c := frozenTestCanvas(t)
	for name, mutate := range map[string]func(*PrintCanvas){
		"hash":            func(c *PrintCanvas) { c.SHA256 = c.SourceSHA256 },
		"dimensions":      func(c *PrintCanvas) { c.Width = 946 },
		"source hash":     func(c *PrintCanvas) { c.SourceSHA256 = "invalid" },
		"scale":           func(c *PrintCanvas) { c.Placement.Scale = 0 },
		"offset":          func(c *PrintCanvas) { c.Placement.OffsetY = math.NaN() },
		"anchor evidence": func(c *PrintCanvas) { c.Placement.AnchorImpossible = true },
	} {
		t.Run(name, func(t *testing.T) {
			bad := *c
			mutate(&bad)
			_, err := (&Session{closed: true}).Render(RenderRequest{Canvas: &bad, PSVersion: "27.7.0", Scales: []float64{1}, OutDir: t.TempDir()})
			if err == nil || errors.Is(err, ErrClosed) {
				t.Fatalf("invalid evidence reached COM: %v", err)
			}
		})
	}
}

func TestPhotoshopFrozenRequiresCompletePNG(t *testing.T) {
	c := frozenTestCanvas(t)
	data, err := os.ReadFile(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	data = data[:len(data)/2]
	if err := os.WriteFile(c.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	c.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	_, err = (&Session{closed: true}).Render(RenderRequest{Canvas: c, PSVersion: "27.7.0", Scales: []float64{1}, OutDir: t.TempDir()})
	if err == nil || errors.Is(err, ErrClosed) {
		t.Fatalf("truncated PNG reached COM: %v", err)
	}
}

func TestFrozenCanvasVerificationOnlyRequiredForNoCrop(t *testing.T) {
	c := frozenTestCanvas(t)
	c.Verified = false
	req := RenderRequest{Canvas: c, PSVersion: "27.7.0", Scales: []float64{1}, OutDir: t.TempDir()}
	if err := c.Validate(); err != nil {
		t.Fatalf("cropped but structurally valid canvas: %v", err)
	}
	if _, err := (&Session{closed: true}).Render(req); !errors.Is(err, ErrClosed) {
		t.Fatalf("manual frozen projection rejected: %v", err)
	}
	req.NoCrop = true
	if _, err := (&Session{closed: true}).Render(req); err == nil || errors.Is(err, ErrClosed) {
		t.Fatalf("unverified no-crop reached COM: %v", err)
	}
}

func TestPrintCanvasValidateChecksDecodedSizeAndNil(t *testing.T) {
	if err := (*PrintCanvas)(nil).Validate(); err == nil {
		t.Fatal("nil evidence accepted")
	}
	c := frozenTestCanvas(t)
	f, err := os.Create(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewNRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	c.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	if err := c.Validate(); err == nil {
		t.Fatal("matching digest for noncanonical decoded size accepted")
	}
}
