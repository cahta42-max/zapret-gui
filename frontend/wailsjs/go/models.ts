export namespace diag {
	
	export class Check {
	    name: string;
	    status: string;
	    detail?: string;
	    url?: string;
	
	    static createFrom(source: any = {}) {
	        return new Check(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.status = source["status"];
	        this.detail = source["detail"];
	        this.url = source["url"];
	    }
	}

}

export namespace domaincheck {
	
	export class Result {
	    domain: string;
	    url: string;
	    withBypass: strattest.UrlTestResult;
	    withoutBypass: strattest.UrlTestResult;
	    verify?: strattest.UrlTestResult;
	    action: string;
	    label: string;
	    message: string;
	    applied: boolean;
	    restarted: boolean;
	    healed: boolean;
	    blocked: boolean;
	    movedFrom: string;
	
	    static createFrom(source: any = {}) {
	        return new Result(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.domain = source["domain"];
	        this.url = source["url"];
	        this.withBypass = this.convertValues(source["withBypass"], strattest.UrlTestResult);
	        this.withoutBypass = this.convertValues(source["withoutBypass"], strattest.UrlTestResult);
	        this.verify = this.convertValues(source["verify"], strattest.UrlTestResult);
	        this.action = source["action"];
	        this.label = source["label"];
	        this.message = source["message"];
	        this.applied = source["applied"];
	        this.restarted = source["restarted"];
	        this.healed = source["healed"];
	        this.blocked = source["blocked"];
	        this.movedFrom = source["movedFrom"];
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

export namespace main {
	
	export class AutoHealStatus {
	    enabled: boolean;
	    dryRun: boolean;
	    active: boolean;
	    mode: string;
	    lastError: string;
	
	    static createFrom(source: any = {}) {
	        return new AutoHealStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.dryRun = source["dryRun"];
	        this.active = source["active"];
	        this.mode = source["mode"];
	        this.lastError = source["lastError"];
	    }
	}
	export class TgwsStatus {
	    installed: boolean;
	    version: string;
	    latestVersion: string;
	    running: boolean;
	    portOpen: boolean;
	    link: string;
	    autostart: boolean;
	
	    static createFrom(source: any = {}) {
	        return new TgwsStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.installed = source["installed"];
	        this.version = source["version"];
	        this.latestVersion = source["latestVersion"];
	        this.running = source["running"];
	        this.portOpen = source["portOpen"];
	        this.link = source["link"];
	        this.autostart = source["autostart"];
	    }
	}
	export class StrategyItem {
	    name: string;
	
	    static createFrom(source: any = {}) {
	        return new StrategyItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	    }
	}
	export class ZapretStatus {
	    installed: boolean;
	    version: string;
	    latestVersion: string;
	    mode: string;
	    running: boolean;
	    currentStrategy: string;
	    autoRun: boolean;
	    strategies: StrategyItem[];
	    serviceState: string;
	    serviceStrategy: string;
	    gameFilter: string;
	    ipset: string;
	    checkUpdatesFlag: boolean;
	    vpnPause: boolean;
	    vpnActive: boolean;
	    vpnName: string;
	    vpnAdapters: string[];
	
	    static createFrom(source: any = {}) {
	        return new ZapretStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.installed = source["installed"];
	        this.version = source["version"];
	        this.latestVersion = source["latestVersion"];
	        this.mode = source["mode"];
	        this.running = source["running"];
	        this.currentStrategy = source["currentStrategy"];
	        this.autoRun = source["autoRun"];
	        this.strategies = this.convertValues(source["strategies"], StrategyItem);
	        this.serviceState = source["serviceState"];
	        this.serviceStrategy = source["serviceStrategy"];
	        this.gameFilter = source["gameFilter"];
	        this.ipset = source["ipset"];
	        this.checkUpdatesFlag = source["checkUpdatesFlag"];
	        this.vpnPause = source["vpnPause"];
	        this.vpnActive = source["vpnActive"];
	        this.vpnName = source["vpnName"];
	        this.vpnAdapters = source["vpnAdapters"];
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
	export class Status {
	    admin: boolean;
	    firstRun: boolean;
	    toolsDir: string;
	    zapret: ZapretStatus;
	    tgws: TgwsStatus;
	    autostartGui: boolean;
	    startMinimized: boolean;
	    appVersion: string;
	    platform: string;
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.admin = source["admin"];
	        this.firstRun = source["firstRun"];
	        this.toolsDir = source["toolsDir"];
	        this.zapret = this.convertValues(source["zapret"], ZapretStatus);
	        this.tgws = this.convertValues(source["tgws"], TgwsStatus);
	        this.autostartGui = source["autostartGui"];
	        this.startMinimized = source["startMinimized"];
	        this.appVersion = source["appVersion"];
	        this.platform = source["platform"];
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

export namespace strattest {
	
	export class UrlTestResult {
	    url: string;
	    ok: boolean;
	    status: number;
	    ms: number;
	    err?: string;
	
	    static createFrom(source: any = {}) {
	        return new UrlTestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.ok = source["ok"];
	        this.status = source["status"];
	        this.ms = source["ms"];
	        this.err = source["err"];
	    }
	}

}

