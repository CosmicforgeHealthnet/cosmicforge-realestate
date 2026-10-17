import { test,expect } from '@playwright/test';
import path from 'node:path';
test('compiled Flutter preview renders with local assets, navigation and themes',async({page})=>{
  const external:string[]=[],errors:string[]=[];
  page.on('request',r=>{const url=new URL(r.url());if(['http:','https:'].includes(url.protocol)&&url.hostname!=='127.0.0.1')external.push(r.url())});
  page.on('pageerror',e=>errors.push(e.message));
  await page.goto('/');
  const semantics=page.getByRole('button',{name:'Enable accessibility'});
  // Flutter's opt-in placeholder is intentionally positioned offscreen.
  await semantics.dispatchEvent('click');
  await expect.poll(()=>page.locator('body').ariaSnapshot()).toContain('3 Portfolio properties');
  await page.screenshot({path:path.resolve('../docs/evidence/flutter-browser-light.png')});
  await page.getByRole('button',{name:'Switch to dark theme'}).click();
  await expect(page.getByRole('button',{name:'Switch to light theme'})).toBeVisible();
  await page.screenshot({path:path.resolve('../docs/evidence/flutter-browser-dark.png')});
  await page.getByRole('tab',{name:'Properties',exact:true}).click();
  await expect(page.getByRole('button',{name:'View sample property overview'})).toBeVisible();
  expect(external).toEqual([]);expect(errors).toEqual([]);
});
