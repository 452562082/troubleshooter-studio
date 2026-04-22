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

export namespace llmchat {
	
	export class Message {
	    role: string;
	    content: string;
	
	    static createFrom(source: any = {}) {
	        return new Message(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.role = source["role"];
	        this.content = source["content"];
	    }
	}

}

export namespace main {
	
	export class ChatContext {
	    system_id: string;
	    system_name: string;
	    model: string;
	    provider_id: string;
	    provider_name: string;
	    envs: string[];
	
	    static createFrom(source: any = {}) {
	        return new ChatContext(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.system_id = source["system_id"];
	        this.system_name = source["system_name"];
	        this.model = source["model"];
	        this.provider_id = source["provider_id"];
	        this.provider_name = source["provider_name"];
	        this.envs = source["envs"];
	    }
	}
	export class ChatSendInput {
	    bot_path: string;
	    api_key: string;
	    messages: llmchat.Message[];
	    default_env: string;
	
	    static createFrom(source: any = {}) {
	        return new ChatSendInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.bot_path = source["bot_path"];
	        this.api_key = source["api_key"];
	        this.messages = this.convertValues(source["messages"], llmchat.Message);
	        this.default_env = source["default_env"];
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

