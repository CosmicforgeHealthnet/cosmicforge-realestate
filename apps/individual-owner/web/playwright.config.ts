import { defineConfig } from '@playwright/test';
export default defineConfig({testDir:'./e2e',testIgnore:['**/flutter/**','**/workflows/**'],fullyParallel:false,workers:1,reporter:'list',use:{baseURL:'http://127.0.0.1:3100',channel:'chrome'},webServer:{command:'npm run start -- --port 3100',url:'http://127.0.0.1:3100',reuseExistingServer:true,timeout:60000}});
