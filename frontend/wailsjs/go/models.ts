export namespace ipa {
	
	export class Account {
	    email: string;
	    name: string;
	    storeFront: string;
	
	    static createFrom(source: any = {}) {
	        return new Account(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.email = source["email"];
	        this.name = source["name"];
	        this.storeFront = source["storeFront"];
	    }
	}
	export class AppView {
	    id: number;
	    bundleId: string;
	    name: string;
	    version: string;
	    price: number;
	    free: boolean;
	    purchasedAt?: string;
	    platforms: string[];
	    artworkUrl?: string;
	    fileSizeBytes?: number;
	    updatedAt?: string;
	
	    static createFrom(source: any = {}) {
	        return new AppView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.bundleId = source["bundleId"];
	        this.name = source["name"];
	        this.version = source["version"];
	        this.price = source["price"];
	        this.free = source["free"];
	        this.purchasedAt = source["purchasedAt"];
	        this.platforms = source["platforms"];
	        this.artworkUrl = source["artworkUrl"];
	        this.fileSizeBytes = source["fileSizeBytes"];
	        this.updatedAt = source["updatedAt"];
	    }
	}
	export class DownloadRequest {
	    appId: number;
	    bundleId: string;
	    platform: string;
	    externalVersionId?: string;
	    outputPath: string;
	    acquireLicense: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DownloadRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.appId = source["appId"];
	        this.bundleId = source["bundleId"];
	        this.platform = source["platform"];
	        this.externalVersionId = source["externalVersionId"];
	        this.outputPath = source["outputPath"];
	        this.acquireLicense = source["acquireLicense"];
	    }
	}
	export class OwnedAppsList {
	    platform: string;
	    totalCount: number;
	    fetchedAt: string;
	    apps: AppView[];
	    cached: boolean;
	    refreshing: boolean;
	    warning?: string;
	
	    static createFrom(source: any = {}) {
	        return new OwnedAppsList(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.platform = source["platform"];
	        this.totalCount = source["totalCount"];
	        this.fetchedAt = source["fetchedAt"];
	        this.apps = this.convertValues(source["apps"], AppView);
	        this.cached = source["cached"];
	        this.refreshing = source["refreshing"];
	        this.warning = source["warning"];
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
	export class OwnedAppsPage {
	    page: number;
	    count: number;
	    totalCount: number;
	    apps: AppView[];
	
	    static createFrom(source: any = {}) {
	        return new OwnedAppsPage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.page = source["page"];
	        this.count = source["count"];
	        this.totalCount = source["totalCount"];
	        this.apps = this.convertValues(source["apps"], AppView);
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
	export class SearchResult {
	    term: string;
	    count: number;
	    apps: AppView[];
	
	    static createFrom(source: any = {}) {
	        return new SearchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.term = source["term"];
	        this.count = source["count"];
	        this.apps = this.convertValues(source["apps"], AppView);
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
	export class Settings {
	    outputDir: string;
	    platform: string;
	    acquireLicense: boolean;
	    revealAfterDownload: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.outputDir = source["outputDir"];
	        this.platform = source["platform"];
	        this.acquireLicense = source["acquireLicense"];
	        this.revealAfterDownload = source["revealAfterDownload"];
	    }
	}
	export class VersionList {
	    app: AppView;
	    latestVersionId: string;
	    versionIdentifiers: string[];
	
	    static createFrom(source: any = {}) {
	        return new VersionList(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.app = this.convertValues(source["app"], AppView);
	        this.latestVersionId = source["latestVersionId"];
	        this.versionIdentifiers = source["versionIdentifiers"];
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
	export class VersionMetadata {
	    displayVersion: string;
	    // Go type: time
	    releaseDate: any;
	
	    static createFrom(source: any = {}) {
	        return new VersionMetadata(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.displayVersion = source["displayVersion"];
	        this.releaseDate = this.convertValues(source["releaseDate"], null);
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

export namespace services {
	
	export class AuthStatus {
	    loggedIn: boolean;
	    email?: string;
	    name?: string;
	    storeFront?: string;
	    message?: string;
	    loggedOut: boolean;
	    expired: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AuthStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.loggedIn = source["loggedIn"];
	        this.email = source["email"];
	        this.name = source["name"];
	        this.storeFront = source["storeFront"];
	        this.message = source["message"];
	        this.loggedOut = source["loggedOut"];
	        this.expired = source["expired"];
	    }
	}
	export class EnvironmentInfo {
	    version: string;
	    initialized: boolean;
	    initError?: string;
	    stateDir?: string;
	    databasePath?: string;
	    cacheDir?: string;
	    outputDir: string;
	    platform: string;
	
	    static createFrom(source: any = {}) {
	        return new EnvironmentInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.initialized = source["initialized"];
	        this.initError = source["initError"];
	        this.stateDir = source["stateDir"];
	        this.databasePath = source["databasePath"];
	        this.cacheDir = source["cacheDir"];
	        this.outputDir = source["outputDir"];
	        this.platform = source["platform"];
	    }
	}
	export class LibraryEntry {
	    id: number;
	    appId: number;
	    bundleId: string;
	    name: string;
	    version: string;
	    platform: string;
	    path: string;
	    sizeBytes: number;
	    sha256: string;
	    downloadedAt: string;
	    createdAt: string;
	    updatedAt: string;
	    exists: boolean;
	    note?: string;
	
	    static createFrom(source: any = {}) {
	        return new LibraryEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.appId = source["appId"];
	        this.bundleId = source["bundleId"];
	        this.name = source["name"];
	        this.version = source["version"];
	        this.platform = source["platform"];
	        this.path = source["path"];
	        this.sizeBytes = source["sizeBytes"];
	        this.sha256 = source["sha256"];
	        this.downloadedAt = source["downloadedAt"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	        this.exists = source["exists"];
	        this.note = source["note"];
	    }
	}
	export class PassphraseStatus {
	    systemStoreAvailable: boolean;
	    hasPassphrase: boolean;
	    expectedBackend: string;
	    pickedBackend: string;
	    matchesExpected: boolean;
	    hint: string;
	
	    static createFrom(source: any = {}) {
	        return new PassphraseStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.systemStoreAvailable = source["systemStoreAvailable"];
	        this.hasPassphrase = source["hasPassphrase"];
	        this.expectedBackend = source["expectedBackend"];
	        this.pickedBackend = source["pickedBackend"];
	        this.matchesExpected = source["matchesExpected"];
	        this.hint = source["hint"];
	    }
	}

}

export namespace store {
	
	export class EntryFilter {
	    Query: string;
	    Platform: string;
	
	    static createFrom(source: any = {}) {
	        return new EntryFilter(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Query = source["Query"];
	        this.Platform = source["Platform"];
	    }
	}
	export class Task {
	    id: string;
	    appId: number;
	    bundleId: string;
	    name: string;
	    platform: string;
	    versionId: string;
	    outputPath: string;
	    state: string;
	    bytesDone: number;
	    bytesTotal: number;
	    errorCode: string;
	    errorMessage: string;
	    createdAt: string;
	    updatedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new Task(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.appId = source["appId"];
	        this.bundleId = source["bundleId"];
	        this.name = source["name"];
	        this.platform = source["platform"];
	        this.versionId = source["versionId"];
	        this.outputPath = source["outputPath"];
	        this.state = source["state"];
	        this.bytesDone = source["bytesDone"];
	        this.bytesTotal = source["bytesTotal"];
	        this.errorCode = source["errorCode"];
	        this.errorMessage = source["errorMessage"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	    }
	}

}

