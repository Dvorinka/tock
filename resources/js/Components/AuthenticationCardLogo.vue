<script setup lang="ts">
import { Link } from '@inertiajs/vue3';
</script>

<template>
    <Link :href="'/'">
        <svg
            class="h-12 py-2 text-text-primary"
            viewBox="0 0 120 32"
            fill="none"
            xmlns="http://www.w3.org/2000/svg">
            <rect x="4" y="4" width="24" height="24" rx="6" fill="rgb(var(--color-accent-500))" />
            <path
                d="M16 9v7l5 3"
                stroke="#18181b"
                stroke-width="2.4"
                stroke-linecap="round"
                stroke-linejoin="round" />
            <text
                x="36"
                y="24"
                font-family="inherit"
                font-size="21"
                font-weight="700"
                letter-spacing="-0.5"
                class="fill-text-primary">tock</text>
        </svg>
    </Link>
</template>
