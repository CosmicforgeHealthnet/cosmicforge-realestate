import {render,screen,cleanup} from '@testing-library/react';
import {describe,it,expect,afterEach} from 'vitest';
import modules from '../../shared/modules.json';
import {WorkspaceExperience} from '@/features/workspace/workspace-experience';
afterEach(()=>cleanup());
describe('premium owner workflow guide',()=>{
 it('provides contextual navigation across all 29 non-dashboard areas',()=>{
  for(const area of modules.filter(m=>m.slug!=='dashboard')){
   const view=render(<WorkspaceExperience slug={area.slug} count={3} statusCount={2} propertyScoped={false}/>);
   expect(view.container.querySelector('section[aria-label="Product workflow guide"]')).not.toBeNull();
   expect(view.container.querySelectorAll('.experience-steps li')).toHaveLength(3);
   const next=view.container.querySelector<HTMLAnchorElement>('.experience-steps>a');
   expect(next?.getAttribute('href')).toMatch(/^\/workspace\/[a-z-]+$/);
   expect(screen.getByText('3')).toBeTruthy();
   view.unmount();
  }
 });
 it('shows the correct property context without implying external completion',()=>{
  render(<WorkspaceExperience slug="sales" count={2} statusCount={1} propertyScoped/>);
  expect(screen.getByText('Property scope applied')).toBeTruthy();
  expect(screen.getByText(/settlement and title transfer separate/i)).toBeTruthy();
  expect(screen.getByRole('link',{name:/Continue to Ownership controls/i})).toHaveAttribute('href','/workspace/ownership');
 });
});