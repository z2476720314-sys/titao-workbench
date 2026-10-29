export namespace cfg {
	
	export class Config {
	    renderer: string;
	    photocraft_cli: string;
	    base_url: string;
	    api_key: string;
	    model: string;
	    tune_model: string;
	    tune_api_key?: string;
	    max_tokens: number;
	    timeout_seconds: number;
	    temperature: number;
	    image_max_side: number;
	    thinking: boolean;
	    candidates: number[];
	    template_dir: string;
	    pattern_dir: string;
	    output_dir: string;
	    group: string;
	    fit: string;
	    cell_size: number;
	    jpg_quality: number;
	    trim_alpha: boolean;
	    no_crop?: boolean;
	    offset_x: number;
	    offset_y: number;
	    max_rounds: number;
	    auto_tune_on: boolean;
	    precheck_on: boolean;
	    precheck_skip: boolean;
	    gen_base_url: string;
	    gen_api_key: string;
	    gen_model: string;
	    gen_size: string;
	    gen_thinking: boolean;
	    gen_timeout: number;
	    gen_count: number;
	    gen_auto_run: boolean;
	    ps_version: string;
	    update_check: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.renderer = source["renderer"];
	        this.photocraft_cli = source["photocraft_cli"];
	        this.base_url = source["base_url"];
	        this.api_key = source.api_key;
	        this.model = source["model"];
	        this.tune_model = source["tune_model"];
	        this.tune_api_key = source.tune_api_key;
	        this.max_tokens = source["max_tokens"];
	        this.timeout_seconds = source["timeout_seconds"];
	        this.temperature = source["temperature"];
	        this.image_max_side = source["image_max_side"];
	        this.thinking = source["thinking"];
	        this.candidates = source["candidates"];
	        this.template_dir = source["template_dir"];
	        this.pattern_dir = source["pattern_dir"];
	        this.output_dir = source["output_dir"];
	        this.group = source["group"];
	        this.fit = source["fit"];
	        this.cell_size = source["cell_size"];
	        this.jpg_quality = source["jpg_quality"];
	        this.trim_alpha = source["trim_alpha"];
	        this.no_crop = source["no_crop"];
	        this.offset_x = source["offset_x"];
	        this.offset_y = source["offset_y"];
	        this.max_rounds = source["max_rounds"];
	        this.auto_tune_on = source["auto_tune_on"];
	        this.precheck_on = source["precheck_on"];
	        this.precheck_skip = source["precheck_skip"];
	        this.gen_base_url = source["gen_base_url"];
	        this.gen_api_key = source.gen_api_key;
	        this.gen_model = source["gen_model"];
	        this.gen_size = source["gen_size"];
	        this.gen_thinking = source["gen_thinking"];
	        this.gen_timeout = source["gen_timeout"];
	        this.gen_count = source["gen_count"];
	        this.gen_auto_run = source["gen_auto_run"];
	        this.ps_version = source["ps_version"];
	        this.update_check = source["update_check"];
	    }
	}

}

export namespace genjobs {
	
	export class GeneratedGroup {
	    name: string;
	    dir: string;
	    count: number;
	    files: string[];
	
	    static createFrom(source: any = {}) {
	        return new GeneratedGroup(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.dir = source["dir"];
	        this.count = source["count"];
	        this.files = source["files"];
	    }
	}
	export class Options {
	    refs: string[];
	    modes: string[];
	    count: number;
	    outputRoot: string;
	
	    static createFrom(source: any = {}) {
	        return new Options(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.refs = source["refs"];
	        this.modes = source["modes"];
	        this.count = source["count"];
	        this.outputRoot = source["outputRoot"];
	    }
	}

}

export namespace imgutil {
	
	export class Rect {
	    X0: number;
	    Y0: number;
	    X1: number;
	    Y1: number;
	
	    static createFrom(source: any = {}) {
	        return new Rect(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.X0 = source["X0"];
	        this.Y0 = source["Y0"];
	        this.X1 = source["X1"];
	        this.Y1 = source["Y1"];
	    }
	}
	export class PatternResult {
	    Path: string;
	    Width: number;
	    Height: number;
	    Trimmed: boolean;
	    OrigBBox?: Rect;
	    OrigWidth: number;
	    OrigHeight: number;
	    BottomContent: boolean;
	    FullBackground: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PatternResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Path = source["Path"];
	        this.Width = source["Width"];
	        this.Height = source["Height"];
	        this.Trimmed = source["Trimmed"];
	        this.OrigBBox = this.convertValues(source["OrigBBox"], Rect);
	        this.OrigWidth = source["OrigWidth"];
	        this.OrigHeight = source["OrigHeight"];
	        this.BottomContent = source["BottomContent"];
	        this.FullBackground = source["FullBackground"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace jobs {
	
	export class Options {
	    patterns: string[];
	    templates: string[];
	    outputDir: string;
	    group: string;
	    fit: string;
	    scales: number[];
	    auto: boolean;
	    trimAlpha: boolean;
	    offsetX: number;
	    offsetY: number;
	    cellSize: number;
	    jpgQuality: number;
	    patternName: string;
	    maxRounds: number;
	    noCrop: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Options(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.patterns = source["patterns"];
	        this.templates = source["templates"];
	        this.outputDir = source["outputDir"];
	        this.group = source["group"];
	        this.fit = source["fit"];
	        this.scales = source["scales"];
	        this.auto = source["auto"];
	        this.trimAlpha = source["trimAlpha"];
	        this.offsetX = source["offsetX"];
	        this.offsetY = source["offsetY"];
	        this.cellSize = source["cellSize"];
	        this.jpgQuality = source["jpgQuality"];
	        this.patternName = source["patternName"];
	        this.maxRounds = source["maxRounds"];
	        this.noCrop = source["noCrop"];
	    }
	}
	export class QueueTask {
	    id: number;
	    pattern: string;
	    template: string;
	    pattern_name: string;
	    template_name: string;
	    phase: string;
	    files?: string[];
	    error?: string;
	    chosen_scale?: number;
	    elapsed_ms?: number;
	    message?: string;
	    judge_note?: string;
	    tune_rounds?: number;
	
	    static createFrom(source: any = {}) {
	        return new QueueTask(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.pattern = source["pattern"];
	        this.template = source["template"];
	        this.pattern_name = source["pattern_name"];
	        this.template_name = source["template_name"];
	        this.phase = source["phase"];
	        this.files = source["files"];
	        this.error = source["error"];
	        this.chosen_scale = source["chosen_scale"];
	        this.elapsed_ms = source["elapsed_ms"];
	        this.message = source["message"];
	        this.judge_note = source["judge_note"];
	        this.tune_rounds = source["tune_rounds"];
	    }
	}
	export class Queue {
	    version: number;
	    created_at: string;
	    updated_at?: string;
	    finished: boolean;
	    pending: number;
	    options: Options;
	    tasks: QueueTask[];
	
	    static createFrom(source: any = {}) {
	        return new Queue(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	        this.finished = source["finished"];
	        this.pending = source["pending"];
	        this.options = this.convertValues(source["options"], Options);
	        this.tasks = this.convertValues(source["tasks"], QueueTask);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class Task {
	    fillTop?: boolean;
	    id: number;
	    pattern: string;
	    patternName: string;
	    template: string;
	    templateName: string;
	    phase: string;
	    progress: number;
	    message: string;
	    outputDir: string;
	    files: string[];
	    error: string;
	    chosenScale: number;
	    judgeNote: string;
	    tuneRounds: number;
	    elapsedMs: number;
	
	    static createFrom(source: any = {}) {
	        return new Task(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fillTop = source["fillTop"];
	        this.id = source["id"];
	        this.pattern = source["pattern"];
	        this.patternName = source["patternName"];
	        this.template = source["template"];
	        this.templateName = source["templateName"];
	        this.phase = source["phase"];
	        this.progress = source["progress"];
	        this.message = source["message"];
	        this.outputDir = source["outputDir"];
	        this.files = source["files"];
	        this.error = source["error"];
	        this.chosenScale = source["chosenScale"];
	        this.judgeNote = source["judgeNote"];
	        this.tuneRounds = source["tuneRounds"];
	        this.elapsedMs = source["elapsedMs"];
	    }
	}

}

export namespace main {
	
	export class FileEntry {
	    name: string;
	    path: string;
	    size: number;
	
	    static createFrom(source: any = {}) {
	        return new FileEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.size = source["size"];
	    }
	}
	export class OutputGroup {
	    name: string;
	    dir: string;
	    count: number;
	    files: string[];
	
	    static createFrom(source: any = {}) {
	        return new OutputGroup(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.dir = source["dir"];
	        this.count = source["count"];
	        this.files = source["files"];
	    }
	}
	export class PSStatus {
	    pending: boolean;
	    engine: string;
	    available: boolean;
	    version: string;
	    error: string;
	    checked: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PSStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pending = source["pending"];
	        this.engine = source["engine"];
	        this.available = source["available"];
	        this.version = source["version"];
	        this.error = source["error"];
	        this.checked = source["checked"];
	    }
	}
	export class UpdateInfo {
	    checksum: string;
	    checked: boolean;
	    hasNew: boolean;
	    current: string;
	    latest: string;
	    notes: string;
	    url: string;
	    asset: string;
	    size: number;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.checksum = source["checksum"];
	        this.checked = source["checked"];
	        this.hasNew = source["hasNew"];
	        this.current = source["current"];
	        this.latest = source["latest"];
	        this.notes = source["notes"];
	        this.url = source["url"];
	        this.asset = source["asset"];
	        this.size = source["size"];
	        this.error = source["error"];
	    }
	}

}

export namespace psx {
	
	export class ArtGroup {
	    group: string;
	    psbs: string[];
	    count: number;
	
	    static createFrom(source: any = {}) {
	        return new ArtGroup(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.group = source["group"];
	        this.psbs = source["psbs"];
	        this.count = source["count"];
	    }
	}
	export class LayerInfo {
	    name: string;
	    kind: string;
	    visible: boolean;
	    opacity: number;
	    blend: string;
	    children: number;
	
	    static createFrom(source: any = {}) {
	        return new LayerInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.kind = source["kind"];
	        this.visible = source["visible"];
	        this.opacity = source["opacity"];
	        this.blend = source["blend"];
	        this.children = source["children"];
	    }
	}
	export class TemplateInfo {
	    name: string;
	    width: number;
	    height: number;
	    cellSize: number;
	    rows: number;
	    guides: number[];
	    groups: string[];
	    layers: LayerInfo[];
	    artGroups: ArtGroup[];
	
	    static createFrom(source: any = {}) {
	        return new TemplateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.cellSize = source["cellSize"];
	        this.rows = source["rows"];
	        this.guides = source["guides"];
	        this.groups = source["groups"];
	        this.layers = this.convertValues(source["layers"], LayerInfo);
	        this.artGroups = this.convertValues(source["artGroups"], ArtGroup);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

