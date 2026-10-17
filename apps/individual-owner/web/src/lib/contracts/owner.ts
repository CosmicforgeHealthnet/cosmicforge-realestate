// Presentation models for the mock foundation, NOT generated public API DTOs.
// Future Gateway adapters must map version-pinned OpenAPI schemas to these models.
export type Scenario = 'populated' | 'empty' | 'error';
export interface Property {
  id: string; name: string; location: string; kind: string; units: number;
  occupied: number; listingCount: number; verification: string; annualRentMinor: number;
}
export interface Task { id: string; title: string; propertyId: string; category: string; done: boolean }
export interface Balance { label: string; amountMinor: number; state: string; description: string }
export interface Dashboard {
  asOf: string; ownerName: string; workspaceName: string;
  properties: Property[]; tasks: Task[]; balances: Balance[];
}
