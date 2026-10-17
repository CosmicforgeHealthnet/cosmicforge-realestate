import { render,screen,fireEvent,waitFor } from '@testing-library/react';
import { it,expect } from 'vitest';
import { Providers } from '@/app/providers';
import { DashboardScreen } from '@/features/dashboard/dashboard-screen';
import { WorkspacePreferences } from '@/features/settings/workspace-preferences';
import { ThemeProvider,useTheme } from '@/design/theme-provider';
it('renders deterministic metrics, updates a task, and recovers from empty/error scenarios',async()=>{
  render(<Providers><DashboardScreen/></Providers>);
  expect(await screen.findByText('Sapphire Court · Demo')).toBeInTheDocument();
  const buttons=screen.getAllByRole('button',{name:'Complete demo task'});
  fireEvent.click(buttons[0]);
  await waitFor(()=>expect(screen.getAllByRole('button',{name:'Reopen demo task'})).toHaveLength(2));
  fireEvent.change(screen.getByLabelText('Demo scenario'),{target:{value:'empty'}});
  expect(await screen.findByText('No sample properties here')).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText('Demo scenario'),{target:{value:'error'}});
  expect(await screen.findByRole('alert')).toHaveTextContent('Simulated repository failure');
  fireEvent.change(screen.getByLabelText('Demo scenario'),{target:{value:'populated'}});
  expect(await screen.findByText('Sapphire Court · Demo')).toBeInTheDocument();
});
it('validates and saves a local preference through RHF, Zod and the repository',async()=>{
  render(<Providers><WorkspacePreferences/></Providers>);
  fireEvent.click(screen.getByRole('button',{name:'Save demo preference'}));
  expect(await screen.findByRole('alert')).toHaveTextContent('at least 2');
  fireEvent.change(screen.getByLabelText('Workspace label'),{target:{value:'Sample owner workspace'}});
  fireEvent.click(screen.getByRole('button',{name:'Save demo preference'}));
  expect(await screen.findByText('Sample owner workspace')).toBeInTheDocument();
});
function ThemeButton(){const {dark,toggle}=useTheme();return <button onClick={toggle}>{dark?'Dark':'Light'}</button>}
it('switches and restores the selected theme',()=>{
  localStorage.clear();const view=render(<ThemeProvider><ThemeButton/></ThemeProvider>);
  fireEvent.click(screen.getByRole('button',{name:'Light'}));
  expect(screen.getByRole('button',{name:'Dark'})).toBeInTheDocument();
  expect(localStorage.getItem('cosmicforge.theme.v1')).toBe('dark');
  view.unmount();render(<ThemeProvider><ThemeButton/></ThemeProvider>);
  expect(screen.getByRole('button',{name:'Dark'})).toBeInTheDocument();localStorage.clear();
});
