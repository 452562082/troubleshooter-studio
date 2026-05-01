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
	
	export class DataStoreUsage {
	    type: string;
	    logical?: string;
	    driver: string;
	    callsite?: string;
	
	    static createFrom(source: any = {}) {
	        return new DataStoreUsage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.logical = source["logical"];
	        this.driver = source["driver"];
	        this.callsite = source["callsite"];
	    }
	}
	export class DownstreamCall {
	    target: string;
	    driver: string;
	    callsite?: string;
	    hint?: string;
	
	    static createFrom(source: any = {}) {
	        return new DownstreamCall(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.target = source["target"];
	        this.driver = source["driver"];
	        this.callsite = source["callsite"];
	        this.hint = source["hint"];
	    }
	}
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
	export class RoleHint {
	    role: string;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new RoleHint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.role = source["role"];
	        this.reason = source["reason"];
	    }
	}
	export class SchemaTable {
	    name: string;
	    kind: string;
	    type?: string;
	    source_file?: string;
	    entity_name?: string;
	    fields?: string[];
	    strategy?: string;
	
	    static createFrom(source: any = {}) {
	        return new SchemaTable(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.kind = source["kind"];
	        this.type = source["type"];
	        this.source_file = source["source_file"];
	        this.entity_name = source["entity_name"];
	        this.fields = source["fields"];
	        this.strategy = source["strategy"];
	    }
	}
	export class RepoAnalysis {
	    name: string;
	    stack: string;
	    repo_path: string;
	    service_names?: string[];
	    findings?: Finding[];
	    downstream_calls?: DownstreamCall[];
	    data_store_usages?: DataStoreUsage[];
	    schema_tables?: SchemaTable[];
	    role_hint?: RoleHint;
	    warnings?: string[];
	    notes?: string[];
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
	        this.downstream_calls = this.convertValues(source["downstream_calls"], DownstreamCall);
	        this.data_store_usages = this.convertValues(source["data_store_usages"], DataStoreUsage);
	        this.schema_tables = this.convertValues(source["schema_tables"], SchemaTable);
	        this.role_hint = this.convertValues(source["role_hint"], RoleHint);
	        this.warnings = source["warnings"];
	        this.notes = source["notes"];
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
	
	
	export class SubmoduleHint {
	    name: string;
	    sub_path: string;
	    stack: string;
	    role: string;
	    reason: string;
	    url?: string;
	
	    static createFrom(source: any = {}) {
	        return new SubmoduleHint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.sub_path = source["sub_path"];
	        this.stack = source["stack"];
	        this.role = source["role"];
	        this.reason = source["reason"];
	        this.url = source["url"];
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
	    detected_framework?: string;
	    branches?: string[];
	    role?: string;
	
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
	        this.detected_framework = source["detected_framework"];
	        this.branches = source["branches"];
	        this.role = source["role"];
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

export namespace config {
	
	export class HealthIssue {
	    severity: string;
	    category: string;
	    field?: string;
	    message: string;
	    hint?: string;
	
	    static createFrom(source: any = {}) {
	        return new HealthIssue(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.severity = source["severity"];
	        this.category = source["category"];
	        this.field = source["field"];
	        this.message = source["message"];
	        this.hint = source["hint"];
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
	    codex?: aitools.Result;
	
	    static createFrom(source: any = {}) {
	        return new AIToolsDetectResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.claude_code = this.convertValues(source["claude_code"], aitools.Result);
	        this.cursor = this.convertValues(source["cursor"], aitools.Result);
	        this.codex = this.convertValues(source["codex"], aitools.Result);
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
	export class FileNode {
	    name: string;
	    path: string;
	    is_dir: boolean;
	    size?: number;
	    children?: FileNode[];
	
	    static createFrom(source: any = {}) {
	        return new FileNode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.is_dir = source["is_dir"];
	        this.size = source["size"];
	        this.children = this.convertValues(source["children"], FileNode);
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
	export class GenPreviewFile {
	    path: string;
	    size: number;
	    binary: boolean;
	    truncated: boolean;
	    content?: string;
	
	    static createFrom(source: any = {}) {
	        return new GenPreviewFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.size = source["size"];
	        this.binary = source["binary"];
	        this.truncated = source["truncated"];
	        this.content = source["content"];
	    }
	}
	export class GenPreviewResult {
	    system: string;
	    config_center: string;
	    targets: string[];
	    skills_included: generator.SkillDecision[];
	    skills_skipped: generator.SkillDecision[];
	    files: GenPreviewFile[];
	
	    static createFrom(source: any = {}) {
	        return new GenPreviewResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.system = source["system"];
	        this.config_center = source["config_center"];
	        this.targets = source["targets"];
	        this.skills_included = this.convertValues(source["skills_included"], generator.SkillDecision);
	        this.skills_skipped = this.convertValues(source["skills_skipped"], generator.SkillDecision);
	        this.files = this.convertValues(source["files"], GenPreviewFile);
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
	export class KuboardNamespace {
	    name: string;
	    configmaps: string[];
	
	    static createFrom(source: any = {}) {
	        return new KuboardNamespace(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.configmaps = source["configmaps"];
	    }
	}
	export class KuboardCluster {
	    name: string;
	    namespaces: KuboardNamespace[];
	
	    static createFrom(source: any = {}) {
	        return new KuboardCluster(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.namespaces = this.convertValues(source["namespaces"], KuboardNamespace);
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
	export class KuboardContainerStat {
	    name: string;
	    image: string;
	    ready: boolean;
	    restart_count: number;
	    state: string;
	    wait_reason?: string;
	    term_reason?: string;
	    term_exit_code?: number;
	
	    static createFrom(source: any = {}) {
	        return new KuboardContainerStat(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.image = source["image"];
	        this.ready = source["ready"];
	        this.restart_count = source["restart_count"];
	        this.state = source["state"];
	        this.wait_reason = source["wait_reason"];
	        this.term_reason = source["term_reason"];
	        this.term_exit_code = source["term_exit_code"];
	    }
	}
	export class KuboardDeploymentInfo {
	    name: string;
	    namespace: string;
	    replicas: number;
	    updated_replicas: number;
	    ready_replicas: number;
	    available_replicas: number;
	    strategy: string;
	    conditions?: string[];
	    selector?: string;
	
	    static createFrom(source: any = {}) {
	        return new KuboardDeploymentInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.namespace = source["namespace"];
	        this.replicas = source["replicas"];
	        this.updated_replicas = source["updated_replicas"];
	        this.ready_replicas = source["ready_replicas"];
	        this.available_replicas = source["available_replicas"];
	        this.strategy = source["strategy"];
	        this.conditions = source["conditions"];
	        this.selector = source["selector"];
	    }
	}
	export class KuboardEvent {
	    type: string;
	    reason: string;
	    message: string;
	    involved_object: string;
	    count: number;
	    first_timestamp: string;
	    last_timestamp: string;
	
	    static createFrom(source: any = {}) {
	        return new KuboardEvent(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.reason = source["reason"];
	        this.message = source["message"];
	        this.involved_object = source["involved_object"];
	        this.count = source["count"];
	        this.first_timestamp = source["first_timestamp"];
	        this.last_timestamp = source["last_timestamp"];
	    }
	}
	export class KuboardFetchBatchItem {
	    key: string;
	    cluster: string;
	    namespace: string;
	    configmap: string;
	
	    static createFrom(source: any = {}) {
	        return new KuboardFetchBatchItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.cluster = source["cluster"];
	        this.namespace = source["namespace"];
	        this.configmap = source["configmap"];
	    }
	}
	export class KuboardFetchBatchInput {
	    url: string;
	    access_key?: string;
	    username?: string;
	    password?: string;
	    items: KuboardFetchBatchItem[];
	
	    static createFrom(source: any = {}) {
	        return new KuboardFetchBatchInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.access_key = source["access_key"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.items = this.convertValues(source["items"], KuboardFetchBatchItem);
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
	
	export class KuboardFetchBatchItemResult {
	    key: string;
	    ok: boolean;
	    content?: string;
	    format?: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new KuboardFetchBatchItemResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.ok = source["ok"];
	        this.content = source["content"];
	        this.format = source["format"];
	        this.error = source["error"];
	    }
	}
	export class KuboardFetchBatchResult {
	    items: KuboardFetchBatchItemResult[];
	    notes?: string[];
	
	    static createFrom(source: any = {}) {
	        return new KuboardFetchBatchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], KuboardFetchBatchItemResult);
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
	export class KuboardGetPodLogsInput {
	    url: string;
	    access_key?: string;
	    username?: string;
	    password?: string;
	    cluster: string;
	    namespace: string;
	    pod_name: string;
	    container?: string;
	    tail_lines?: number;
	    previous?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new KuboardGetPodLogsInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.access_key = source["access_key"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.cluster = source["cluster"];
	        this.namespace = source["namespace"];
	        this.pod_name = source["pod_name"];
	        this.container = source["container"];
	        this.tail_lines = source["tail_lines"];
	        this.previous = source["previous"];
	    }
	}
	export class KuboardListEventsInput {
	    url: string;
	    access_key?: string;
	    username?: string;
	    password?: string;
	    cluster: string;
	    namespace: string;
	    field_selector?: string;
	    only_warnings?: boolean;
	    limit?: number;
	
	    static createFrom(source: any = {}) {
	        return new KuboardListEventsInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.access_key = source["access_key"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.cluster = source["cluster"];
	        this.namespace = source["namespace"];
	        this.field_selector = source["field_selector"];
	        this.only_warnings = source["only_warnings"];
	        this.limit = source["limit"];
	    }
	}
	export class KuboardListPodsInput {
	    url: string;
	    access_key?: string;
	    username?: string;
	    password?: string;
	    cluster: string;
	    namespace: string;
	    label_selector?: string;
	    pod_name_filter?: string;
	
	    static createFrom(source: any = {}) {
	        return new KuboardListPodsInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.access_key = source["access_key"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.cluster = source["cluster"];
	        this.namespace = source["namespace"];
	        this.label_selector = source["label_selector"];
	        this.pod_name_filter = source["pod_name_filter"];
	    }
	}
	
	export class KuboardPodInfo {
	    name: string;
	    namespace: string;
	    status: string;
	    phase: string;
	    node_name: string;
	    pod_ip: string;
	    start_time: string;
	    restart_count: number;
	    containers: KuboardContainerStat[];
	    reason?: string;
	    message?: string;
	
	    static createFrom(source: any = {}) {
	        return new KuboardPodInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.namespace = source["namespace"];
	        this.status = source["status"];
	        this.phase = source["phase"];
	        this.node_name = source["node_name"];
	        this.pod_ip = source["pod_ip"];
	        this.start_time = source["start_time"];
	        this.restart_count = source["restart_count"];
	        this.containers = this.convertValues(source["containers"], KuboardContainerStat);
	        this.reason = source["reason"];
	        this.message = source["message"];
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
	export class KuboardPodSnapshotEntry {
	    pod: KuboardPodInfo;
	    events?: KuboardEvent[];
	    logs_current?: string;
	    logs_previous?: string;
	
	    static createFrom(source: any = {}) {
	        return new KuboardPodSnapshotEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pod = this.convertValues(source["pod"], KuboardPodInfo);
	        this.events = this.convertValues(source["events"], KuboardEvent);
	        this.logs_current = source["logs_current"];
	        this.logs_previous = source["logs_previous"];
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
	export class KuboardPodSnapshotInput {
	    url: string;
	    access_key?: string;
	    username?: string;
	    password?: string;
	    cluster: string;
	    namespace: string;
	    label_selector?: string;
	    pod_name_filter?: string;
	    tail_lines?: number;
	
	    static createFrom(source: any = {}) {
	        return new KuboardPodSnapshotInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.access_key = source["access_key"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.cluster = source["cluster"];
	        this.namespace = source["namespace"];
	        this.label_selector = source["label_selector"];
	        this.pod_name_filter = source["pod_name_filter"];
	        this.tail_lines = source["tail_lines"];
	    }
	}
	export class KuboardPodSnapshotResult {
	    pods: KuboardPodSnapshotEntry[];
	    notes?: string[];
	
	    static createFrom(source: any = {}) {
	        return new KuboardPodSnapshotResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pods = this.convertValues(source["pods"], KuboardPodSnapshotEntry);
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
	export class KuboardResources {
	    clusters: KuboardCluster[];
	    notes?: string[];
	
	    static createFrom(source: any = {}) {
	        return new KuboardResources(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.clusters = this.convertValues(source["clusters"], KuboardCluster);
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
	export class KuboardServiceInfo {
	    name: string;
	    namespace: string;
	    cluster_ip: string;
	    type: string;
	    ports: string[];
	    selector?: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new KuboardServiceInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.namespace = source["namespace"];
	        this.cluster_ip = source["cluster_ip"];
	        this.type = source["type"];
	        this.ports = source["ports"];
	        this.selector = source["selector"];
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
	export class MissingRepoPathsResult {
	    system_id: string;
	    saved: Record<string, string>;
	    missing: string[];
	    suggest_repos_root: string;
	
	    static createFrom(source: any = {}) {
	        return new MissingRepoPathsResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.system_id = source["system_id"];
	        this.saved = source["saved"];
	        this.missing = source["missing"];
	        this.suggest_repos_root = source["suggest_repos_root"];
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
	export class ReadFileResult {
	    content: string;
	    is_binary: boolean;
	    truncated?: boolean;
	    size: number;
	
	    static createFrom(source: any = {}) {
	        return new ReadFileResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.content = source["content"];
	        this.is_binary = source["is_binary"];
	        this.truncated = source["truncated"];
	        this.size = source["size"];
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
	export class UninstallBotResult {
	    target: string;
	    workspace_moved_to?: string;
	    openclaw_json_clean?: boolean;
	    creds_removed?: boolean;
	    staging_moved_to?: string;
	    user_agent_md?: string;
	    user_skills_dir?: string;
	    user_scripts_dir?: string;
	    log?: string[];
	
	    static createFrom(source: any = {}) {
	        return new UninstallBotResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.target = source["target"];
	        this.workspace_moved_to = source["workspace_moved_to"];
	        this.openclaw_json_clean = source["openclaw_json_clean"];
	        this.creds_removed = source["creds_removed"];
	        this.staging_moved_to = source["staging_moved_to"];
	        this.user_agent_md = source["user_agent_md"];
	        this.user_skills_dir = source["user_skills_dir"];
	        this.user_scripts_dir = source["user_scripts_dir"];
	        this.log = source["log"];
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
	    issues?: config.HealthIssue[];
	
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
	        this.issues = this.convertValues(source["issues"], config.HealthIssue);
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

