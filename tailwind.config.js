import defaultTheme from 'tailwindcss/defaultTheme';
import forms from '@tailwindcss/forms';
import typography from '@tailwindcss/typography';
import { solidtimeTheme } from './resources/js/packages/ui/tailwind.theme.js';

/** @type {import("tailwindcss").Config} */
export default {
    darkMode: ['selector', '.dark'],
    content: [
        './resources/js/**/*.vue',
        './resources/js/**/*.ts',
        '!./resources/js/**/node_modules',
    ],
    theme: {
        extend: {
            ...solidtimeTheme,
            fontFamily: {
                sans: ['Space Grotesk', ...defaultTheme.fontFamily.sans],
            },
        },
    },

    plugins: [
        forms,
        typography,
        require('@tailwindcss/container-queries'),
        require('tailwindcss-animate'),
    ],
};
