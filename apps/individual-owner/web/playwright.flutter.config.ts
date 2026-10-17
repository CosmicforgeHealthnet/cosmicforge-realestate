import { defineConfig } from '@playwright/test';
export default defineConfig({testDir:'./e2e/flutter',workers:1,reporter:'list',timeout:60000,use:{baseURL:'http://127.0.0.1:4173',channel:'chrome',viewport:{width:390,height:844}},webServer:{command:'python -m http.server 4173 --bind 127.0.0.1 --directory ../mobile/build/web',url:'http://127.0.0.1:4173',reuseExistingServer:true,timeout:30000}});
