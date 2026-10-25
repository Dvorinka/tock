import { defineConfig } from 'vite';
import laravel from 'laravel-vite-plugin';
import vue from '@vitejs/plugin-vue';
import checker from 'vite-plugin-checker';

export default defineConfig({
    base: './',
    build: {
        sourcemap: true, // Source map generation must be turned on
    },
    plugins: [
        laravel({
            input: ['resources/js/app.ts', 'resources/css/app.css'],
            refresh: true,
        }),
        vue({
            template: {
                transformAssetUrls: {
                    base: null,
                    includeAbsolute: false,
                },
            },
        }),
        checker({
            // e.g. use TypeScript check
            typescript: true,
            vueTsc: true,
            lintCommand: 'eslint "./**/*.{ts,vue}"',
        }),
    ],
    server: {
        host: true,
        hmr: {
            host: process.env.VITE_HOST_NAME,
            clientPort: 80,
        },
    },
});
