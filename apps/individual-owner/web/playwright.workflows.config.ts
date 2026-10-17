import {defineConfig} from '@playwright/test';
export default defineConfig({testDir:'./e2e/workflows',workers:1,reporter:'list',timeout:180000,use:{actionTimeout:10000,navigationTimeout:15000,baseURL:'http://127.0.0.1:3100',channel:'chrome',viewport:{width:1440,height:1100}},webServer:{command:'npm run start -- --port 3100',url:'http://127.0.0.1:3100',reuseExistingServer:true,timeout:30000}});
