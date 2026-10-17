export type Value=string|number|boolean;
export type Values=Record<string,Value>;
export interface FieldSpec {key:string;label:string;kind:string;required:boolean;target?:string;options?:string[];min?:number;max?:number;pattern?:string}
export interface EffectSpec {type:string;status:string;data:Values}
export interface ActionSpec {id:string;label:string;from:string[];to:string;confirm?:boolean;stepUp?:boolean;financial?:boolean;guard?:string;copy?:boolean;requires?:{kind:string;key:string;message:string}[];effects?:EffectSpec[];updateRelated?:{via:string;type:string;sets:Values;status?:string}}
export interface ModelSpec {id:string;label:string;module:string;fields:FieldSpec[];initial:string;actions:ActionSpec[];sensitive?:boolean;wizard?:boolean;calendar?:boolean;guard?:string;readOnly?:boolean;provider?:string}
export interface ModuleSpec {id:string;title:string;slug:string;gate:string;pairIds:string[];types:string[]}
export interface WorkspaceSchema {version:number;date:string;scenarios:string[];modules:ModuleSpec[];types:ModelSpec[]}
export interface DomainRecord {id:string;type:string;status:string;version:number;createdAt:string;data:Values}
export interface Activity {id:string;type:string;recordId:string;propertyId:string;date:string;message:string;revision:number}
export interface WorkspaceState {revision:number;scenario:string;actor:string;date:string;records:DomainRecord[];activity:Activity[];tasks:{id:string;title:string;propertyId:string;category:string;done:boolean}[];workspaceName:string}
export interface WorkspaceCommand {op:'create'|'update'|'transition';type:string;id?:string;version?:number;data?:Values;action?:string;confirmed?:boolean;challenge?:string}
export interface WorkspaceRepository {readWorkspace():Promise<WorkspaceState>;execute(command:WorkspaceCommand):Promise<DomainRecord>;setScenario(scenario:string):Promise<void>;setActor(actor:string):Promise<void>;resetDemo():Promise<void>}
