'use client';
import { useParams } from 'next/navigation';
import Link from 'next/link';
import { useDashboard } from '@/features/dashboard/use-dashboard';
import { LoadingState, ErrorState, EmptyState } from '@/components/states';
import { PropertyArt } from '@/components/property-art';
import { money } from '@/lib/utils/format';
export default function PropertyPage() {
  const {id} = useParams<{id:string}>(); const {data,isPending,error} = useDashboard();
  if (isPending) return <LoadingState/>;
  if (error) return <ErrorState message={error.message}/>;
  const property = data?.properties.find(p=>p.id===id);
  if (!property) return <EmptyState title="Sample property not found">Return to the dashboard to select an existing sample.</EmptyState>;
  return <><Link className="back-link" href="/">← Dashboard</Link><div className="page-heading"><div><p className="eyebrow">FICTIONAL PROPERTY · FOUNDATION OVERVIEW</p><h1>{property.name}</h1><p>{property.location}</p></div><span className="status warning">{property.verification}</span></div><section className="card property-detail"><PropertyArt/><div><h2>Sample property overview</h2><dl><dt>Type</dt><dd>{property.kind}</dd><dt>Units</dt><dd>{property.units}</dd><dt>Sample occupied units</dt><dd>{property.occupied}</dd><dt>Illustrative annual rent</dt><dd>{money(property.annualRentMinor)}</dd></dl><p>No ownership or legal title has been verified. Documents, listings and financial actions follow in later sections.</p><button disabled>Ownership verification unavailable</button></div></section></>;
}
