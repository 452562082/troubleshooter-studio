export namespace agent {
	
	export class Result {
	    agent_path: string;
	    target: string;
	    files_written: number;
	    files_preserved?: string[];
	    files_removed?: string[];
	    tsf_json_updated: boolean;
	    needs_restart_hint?: string;
	
	    static createFrom(source: any = {}) {
	        return new Result(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.agent_path = source["agent_path"];
	        this.target = source["target"];
	        this.files_written = source["files_written"];
	        this.files_preserved = source["files_preserved"];
	        this.files_removed = source["files_removed"];
	        this.tsf_json_updated = source["tsf_json_updated"];
	        this.needs_restart_hint = source["needs_restart_hint"];
	    }
	}
	export class SelfTestCheck {
	    name: string;
	    status: string;
	    detail: string;
	
	    static createFrom(source: any = {}) {
	        return new SelfTestCheck(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.status = source["status"];
	        this.detail = source["detail"];
	    }
	}
	export class SelfTestResult {
	    checks: SelfTestCheck[];
	    ok: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SelfTestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.checks = this.convertValues(source["checks"], SelfTestCheck);
	        this.ok = source["ok"];
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
	export class UninstallOpenclawResult {
	    WorkspaceMovedTo: string;
	    OpenclawJSONClean: boolean;
	    CredsRemoved: boolean;
	    Log: string[];
	
	    static createFrom(source: any = {}) {
	        return new UninstallOpenclawResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.WorkspaceMovedTo = source["WorkspaceMovedTo"];
	        this.OpenclawJSONClean = source["OpenclawJSONClean"];
	        this.CredsRemoved = source["CredsRemoved"];
	        this.Log = source["Log"];
	    }
	}

}

export namespace aitools {
	
	export class Result {
	    installed: boolean;
	    version?: string;
	    path?: string;
	    note?: string;
	
	    static createFrom(source: any = {}) {
	        return new Result(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.installed = source["installed"];
	        this.version = source["version"];
	        this.path = source["path"];
	        this.note = source["note"];
	    }
	}

}

export namespace analyzer {
	
	export class Finding {
	    config_center: string;
	    source_file: string;
	    env_profile?: string;
	    server_addr?: string;
	    data_id?: string;
	    group?: string;
	    namespace_id?: string;
	    app_id?: string;
	    namespaces?: string[];
	    cluster?: string;
	    kv_prefix?: string;
	    default_context?: string;
	
	    static createFrom(source: any = {}) {
	        return new Finding(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.config_center = source["config_center"];
	        this.source_file = source["source_file"];
	        this.env_profile = source["env_profile"];
	        this.server_addr = source["server_addr"];
	        this.data_id = source["data_id"];
	        this.group = source["group"];
	        this.namespace_id = source["namespace_id"];
	        this.app_id = source["app_id"];
	        this.namespaces = source["namespaces"];
	        this.cluster = source["cluster"];
	        this.kv_prefix = source["kv_prefix"];
	        this.default_context = source["default_context"];
	    }
	}
	export class RepoAnalysis {
	    name: string;
	    stack: string;
	    repo_path: string;
	    service_names?: string[];
	    findings?: Finding[];
	    warnings?: string[];
	    verified: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RepoAnalysis(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.stack = source["stack"];
	        this.repo_path = source["repo_path"];
	        this.service_names = source["service_names"];
	        this.findings = this.convertValues(source["findings"], Finding);
	        this.warnings = source["warnings"];
	        this.verified = source["verified"];
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
	export class Report {
	    schema_version: string;
	    config_center: string;
	    repos: RepoAnalysis[];
	
	    static createFrom(source: any = {}) {
	        return new Report(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.schema_version = source["schema_version"];
	        this.config_center = source["config_center"];
	        this.repos = this.convertValues(source["repos"], RepoAnalysis);
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

export namespace analyzerpipe {
	
	export class RepoSummary {
	    name: string;
	    status: string;
	    service_name_count: number;
	    finding_count: number;
	    error?: string;
	    detected_stack?: string;
	    detected_role?: string;
	    detected_framework?: string;
	    branches?: string[];
	
	    static createFrom(source: any = {}) {
	        return new RepoSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.status = source["status"];
	        this.service_name_count = source["service_name_count"];
	        this.finding_count = source["finding_count"];
	        this.error = source["error"];
	        this.detected_stack = source["detected_stack"];
	        this.detected_role = source["detected_role"];
	        this.detected_framework = source["detected_framework"];
	        this.branches = source["branches"];
	    }
	}
	export class Result {
	    report: analyzer.Report;
	    per_repo: RepoSummary[];
	
	    static createFrom(source: any = {}) {
	        return new Result(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.report = this.convertValues(source["report"], analyzer.Report);
	        this.per_repo = this.convertValues(source["per_repo"], RepoSummary);
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

export namespace cchub {
	
	export class Entry {
	    locator: string;
	    group?: string;
	    tenant?: string;
	    type?: string;
	    app_id?: string;
	
	    static createFrom(source: any = {}) {
	        return new Entry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.locator = source["locator"];
	        this.group = source["group"];
	        this.tenant = source["tenant"];
	        this.type = source["type"];
	        this.app_id = source["app_id"];
	    }
	}
	export class FetchBatchItem {
	    key: string;
	    namespace?: string;
	    group?: string;
	    data_id: string;
	    app_id?: string;
	
	    static createFrom(source: any = {}) {
	        return new FetchBatchItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.namespace = source["namespace"];
	        this.group = source["group"];
	        this.data_id = source["data_id"];
	        this.app_id = source["app_id"];
	    }
	}
	export class FetchContentResult {
	    content: string;
	    format?: string;
	    notes?: string[];
	
	    static createFrom(source: any = {}) {
	        return new FetchContentResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.content = source["content"];
	        this.format = source["format"];
	        this.notes = source["notes"];
	    }
	}
	export class FetchBatchItemResult {
	    key: string;
	    ok: boolean;
	    result?: FetchContentResult;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new FetchBatchItemResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.ok = source["ok"];
	        this.result = this.convertValues(source["result"], FetchContentResult);
	        this.error = source["error"];
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
	export class FetchBatchResult {
	    items: FetchBatchItemResult[];
	    notes?: string[];
	
	    static createFrom(source: any = {}) {
	        return new FetchBatchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], FetchBatchItemResult);
	        this.notes = source["notes"];
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
	
	export class Namespace {
	    id: string;
	    show_name: string;
	
	    static createFrom(source: any = {}) {
	        return new Namespace(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.show_name = source["show_name"];
	    }
	}
	export class Result {
	    type: string;
	    entries: Entry[];
	    namespaces?: Namespace[];
	    notes?: string[];
	
	    static createFrom(source: any = {}) {
	        return new Result(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.entries = this.convertValues(source["entries"], Entry);
	        this.namespaces = this.convertValues(source["namespaces"], Namespace);
	        this.notes = source["notes"];
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

export namespace deploy {
	
	export class Prompt {
	    name: string;
	    prompt: string;
	    secret: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Prompt(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.prompt = source["prompt"];
	        this.secret = source["secret"];
	    }
	}

}

export namespace discover {
	
	export class Meta {
	    schema_version: number;
	    tshoot_version: string;
	    system_id: string;
	    system_name: string;
	    target: string;
	    generated_at: string;
	    system_yaml: string;
	    user_edits?: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new Meta(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.schema_version = source["schema_version"];
	        this.tshoot_version = source["tshoot_version"];
	        this.system_id = source["system_id"];
	        this.system_name = source["system_name"];
	        this.target = source["target"];
	        this.generated_at = source["generated_at"];
	        this.system_yaml = source["system_yaml"];
	        this.user_edits = source["user_edits"];
	    }
	}
	export class DiscoveredAgent {
	    meta: Meta;
	    path: string;
	    mod_time: string;
	    env_count: number;
	    repo_count: number;
	    skill_count: number;
	    targets?: string[];
	
	    static createFrom(source: any = {}) {
	        return new DiscoveredAgent(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.meta = this.convertValues(source["meta"], Meta);
	        this.path = source["path"];
	        this.mod_time = source["mod_time"];
	        this.env_count = source["env_count"];
	        this.repo_count = source["repo_count"];
	        this.skill_count = source["skill_count"];
	        this.targets = source["targets"];
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

export namespace doctor {
	
	export class Issue {
	    severity: string;
	    category: string;
	    target: string;
	    message: string;
	    suggest?: string;
	    fix_key?: string;
	    fix_value?: string;
	
	    static createFrom(source: any = {}) {
	        return new Issue(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.severity = source["severity"];
	        this.category = source["category"];
	        this.target = source["target"];
	        this.message = source["message"];
	        this.suggest = source["suggest"];
	        this.fix_key = source["fix_key"];
	        this.fix_value = source["fix_value"];
	    }
	}
	export class Report {
	    issues: Issue[];
	
	    static createFrom(source: any = {}) {
	        return new Report(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.issues = this.convertValues(source["issues"], Issue);
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

export namespace dsprobe {
	
	export class Result {
	    ok: boolean;
	    latency?: string;
	    detail?: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new Result(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.latency = source["latency"];
	        this.detail = source["detail"];
	        this.error = source["error"];
	    }
	}

}

export namespace generator {
	
	export class AnalyzerHitRef {
	    service: string;
	    env?: string;
	    source?: string;
	
	    static createFrom(source: any = {}) {
	        return new AnalyzerHitRef(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.service = source["service"];
	        this.env = source["env"];
	        this.source = source["source"];
	    }
	}
	export class ConfigMapProjection {
	    verified_from_analyzer: number;
	    verified_from_prior: number;
	    inferred: number;
	    total: number;
	
	    static createFrom(source: any = {}) {
	        return new ConfigMapProjection(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.verified_from_analyzer = source["verified_from_analyzer"];
	        this.verified_from_prior = source["verified_from_prior"];
	        this.inferred = source["inferred"];
	        this.total = source["total"];
	    }
	}
	export class GenSummary {
	    system: string;
	    config_center: string;
	    output_dir: string;
	    skills_included_count: number;
	    files_written: number;
	    preserved_count: number;
	    prior_overrides_count: number;
	    analyzer_hits_count: number;
	
	    static createFrom(source: any = {}) {
	        return new GenSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.system = source["system"];
	        this.config_center = source["config_center"];
	        this.output_dir = source["output_dir"];
	        this.skills_included_count = source["skills_included_count"];
	        this.files_written = source["files_written"];
	        this.preserved_count = source["preserved_count"];
	        this.prior_overrides_count = source["prior_overrides_count"];
	        this.analyzer_hits_count = source["analyzer_hits_count"];
	    }
	}
	export class OverrideRef {
	    env: string;
	    service: string;
	
	    static createFrom(source: any = {}) {
	        return new OverrideRef(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.env = source["env"];
	        this.service = source["service"];
	    }
	}
	export class SkillDecision {
	    name: string;
	    reason?: string;
	
	    static createFrom(source: any = {}) {
	        return new SkillDecision(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.reason = source["reason"];
	    }
	}
	export class Plan {
	    system: string;
	    config_center: string;
	    skills_included: SkillDecision[];
	    skills_skipped: SkillDecision[];
	    files_create: string[];
	    files_modify: string[];
	    files_remove: string[];
	    preserved: string[];
	    prior_overrides: OverrideRef[];
	    analyzer_hits: AnalyzerHitRef[];
	    config_map_projection: ConfigMapProjection;
	
	    static createFrom(source: any = {}) {
	        return new Plan(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.system = source["system"];
	        this.config_center = source["config_center"];
	        this.skills_included = this.convertValues(source["skills_included"], SkillDecision);
	        this.skills_skipped = this.convertValues(source["skills_skipped"], SkillDecision);
	        this.files_create = source["files_create"];
	        this.files_modify = source["files_modify"];
	        this.files_remove = source["files_remove"];
	        this.preserved = source["preserved"];
	        this.prior_overrides = this.convertValues(source["prior_overrides"], OverrideRef);
	        this.analyzer_hits = this.convertValues(source["analyzer_hits"], AnalyzerHitRef);
	        this.config_map_projection = this.convertValues(source["config_map_projection"], ConfigMapProjection);
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

export namespace labelprobe {
	
	export class Datasource {
	    uid: string;
	    name: string;
	    type: string;
	    url?: string;
	    is_loki: boolean;
	    default?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Datasource(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.uid = source["uid"];
	        this.name = source["name"];
	        this.type = source["type"];
	        this.url = source["url"];
	        this.is_loki = source["is_loki"];
	        this.default = source["default"];
	    }
	}
	export class LabelsResult {
	    labels: string[];
	    notes?: string[];
	
	    static createFrom(source: any = {}) {
	        return new LabelsResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.labels = source["labels"];
	        this.notes = source["notes"];
	    }
	}
	export class ValuesResult {
	    key: string;
	    values: string[];
	    notes?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ValuesResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.values = source["values"];
	        this.notes = source["notes"];
	    }
	}

}

export namespace main {
	
	export class AIToolsDetectResult {
	    claude_code?: aitools.Result;
	    cursor?: aitools.Result;
	
	    static createFrom(source: any = {}) {
	        return new AIToolsDetectResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.claude_code = this.convertValues(source["claude_code"], aitools.Result);
	        this.cursor = this.convertValues(source["cursor"], aitools.Result);
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
	export class AnalyzeInput {
	    yaml_text: string;
	    repos_root: string;
	    repo_paths?: Record<string, string>;
	    auto_clone: boolean;
	    repo_name?: string;
	
	    static createFrom(source: any = {}) {
	        return new AnalyzeInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.yaml_text = source["yaml_text"];
	        this.repos_root = source["repos_root"];
	        this.repo_paths = source["repo_paths"];
	        this.auto_clone = source["auto_clone"];
	        this.repo_name = source["repo_name"];
	    }
	}
	export class CCHubFetchBatchInput {
	    type: string;
	    addr: string;
	    username?: string;
	    password?: string;
	    token?: string;
	    items: cchub.FetchBatchItem[];
	
	    static createFrom(source: any = {}) {
	        return new CCHubFetchBatchInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.addr = source["addr"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.token = source["token"];
	        this.items = this.convertValues(source["items"], cchub.FetchBatchItem);
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
	export class CCHubFetchContentInput {
	    type: string;
	    addr: string;
	    username?: string;
	    password?: string;
	    token?: string;
	    namespace?: string;
	    group?: string;
	    data_id: string;
	    app_id?: string;
	
	    static createFrom(source: any = {}) {
	        return new CCHubFetchContentInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.addr = source["addr"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.token = source["token"];
	        this.namespace = source["namespace"];
	        this.group = source["group"];
	        this.data_id = source["data_id"];
	        this.app_id = source["app_id"];
	    }
	}
	export class CCHubPreloadInput {
	    type: string;
	    addr: string;
	    username?: string;
	    password?: string;
	    token?: string;
	    namespace?: string;
	    app_id?: string;
	    namespaces_only?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CCHubPreloadInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.addr = source["addr"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.token = source["token"];
	        this.namespace = source["namespace"];
	        this.app_id = source["app_id"];
	        this.namespaces_only = source["namespaces_only"];
	    }
	}
	export class ChatLoadKeyResult {
	    api_key: string;
	    ok: boolean;
	    err?: string;
	
	    static createFrom(source: any = {}) {
	        return new ChatLoadKeyResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.api_key = source["api_key"];
	        this.ok = source["ok"];
	        this.err = source["err"];
	    }
	}
	export class DSProbeInput {
	    type: string;
	    fields: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new DSProbeInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.fields = source["fields"];
	    }
	}
	export class InfraCredBatchInput {
	    entries: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new InfraCredBatchInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.entries = source["entries"];
	    }
	}
	export class LokiAuthInput {
	    grafana_url?: string;
	    loki_url?: string;
	    ds_uid?: string;
	    api_key?: string;
	    user?: string;
	    pass?: string;
	
	    static createFrom(source: any = {}) {
	        return new LokiAuthInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.grafana_url = source["grafana_url"];
	        this.loki_url = source["loki_url"];
	        this.ds_uid = source["ds_uid"];
	        this.api_key = source["api_key"];
	        this.user = source["user"];
	        this.pass = source["pass"];
	    }
	}
	export class OpenClawDetectResult {
	    ok: boolean;
	    installed: boolean;
	    installed_but_empty: boolean;
	    install_dir?: string;
	    config_path?: string;
	    version?: string;
	    models?: openclaw.ModelEntry[];
	    auth_providers?: string[];
	    err?: string;
	
	    static createFrom(source: any = {}) {
	        return new OpenClawDetectResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.installed = source["installed"];
	        this.installed_but_empty = source["installed_but_empty"];
	        this.install_dir = source["install_dir"];
	        this.config_path = source["config_path"];
	        this.version = source["version"];
	        this.models = this.convertValues(source["models"], openclaw.ModelEntry);
	        this.auth_providers = source["auth_providers"];
	        this.err = source["err"];
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
	export class OpenYAMLResult {
	    path: string;
	    content: string;
	
	    static createFrom(source: any = {}) {
	        return new OpenYAMLResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.content = source["content"];
	    }
	}
	export class RunInstallResult {
	    log: string;
	    exit_code: number;
	    ok: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RunInstallResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.log = source["log"];
	        this.exit_code = source["exit_code"];
	        this.ok = source["ok"];
	    }
	}
	export class UserConfigResult {
	    default_repos_root: string;
	    resolved_repos_root: string;
	    home_dir: string;
	
	    static createFrom(source: any = {}) {
	        return new UserConfigResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.default_repos_root = source["default_repos_root"];
	        this.resolved_repos_root = source["resolved_repos_root"];
	        this.home_dir = source["home_dir"];
	    }
	}
	export class ValidateResult {
	    valid: boolean;
	    system: string;
	    name: string;
	    envs: number;
	    repos: number;
	
	    static createFrom(source: any = {}) {
	        return new ValidateResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.valid = source["valid"];
	        this.system = source["system"];
	        this.name = source["name"];
	        this.envs = source["envs"];
	        this.repos = source["repos"];
	    }
	}

}

export namespace openclaw {
	
	export class ModelEntry {
	    id: string;
	    provider?: string;
	    label?: string;
	    source?: string;
	    primary?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ModelEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.provider = source["provider"];
	        this.label = source["label"];
	        this.source = source["source"];
	        this.primary = source["primary"];
	    }
	}

}

