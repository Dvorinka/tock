<script setup lang="ts">
import AppLayout from '@/Layouts/AppLayout.vue';
import MainContainer from '@/packages/ui/src/MainContainer.vue';
import PageTitle from '@/Components/Common/PageTitle.vue';
import SecondaryButton from '@/packages/ui/src/Buttons/SecondaryButton.vue';
import TableHeading from '@/Components/Common/TableHeading.vue';
import TableRow from '@/Components/TableRow.vue';
import Modal from '@/packages/ui/src/Modal.vue';
import Pagination from '@/packages/ui/src/Pagination.vue';
import DatePicker from '@/packages/ui/src/Input/DatePicker.vue';
import Badge from '@/packages/ui/src/Badge.vue';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/packages/ui/src';
import { ArrowPathIcon, ShieldCheckIcon } from '@heroicons/vue/20/solid';
import { computed, ref, watch } from 'vue';
import { useMembersQuery } from '@/utils/useMembersQuery';
import { useAuditsQuery, type AuditFilters } from '@/utils/useAuditsQuery';
import {
    auditChanges,
    auditEventDotClass,
    auditEventOptions,
    auditFieldLabel,
    auditModelOptions,
    formatAuditTimestamp,
} from '@/utils/audits';
import type { Audit } from '@/packages/api/src';

const page = ref(1);
const filters = ref<AuditFilters>({});
const selectedAudit = ref<Audit | null>(null);

// Selects cannot hold an empty value, so "all" is the unfiltered sentinel.
const modelFilter = ref('all');
const eventFilter = ref('all');
const actorFilter = ref('all');

const { audits, total, isLoading } = useAuditsQuery(filters, page);
const { members } = useMembersQuery();

const actorOptions = computed(() =>
    members.value
        .filter((member) => !member.is_placeholder)
        .map((member) => ({ value: member.user_id, label: member.name || member.email }))
);

watch([modelFilter, eventFilter, actorFilter], () => {
    filters.value = {
        ...filters.value,
        model: modelFilter.value === 'all' ? undefined : modelFilter.value,
        event: eventFilter.value === 'all' ? undefined : eventFilter.value,
        actor: actorFilter.value === 'all' ? undefined : actorFilter.value,
    };
});

// DatePicker yields a localized timestamp; the API filters by whole days.
const from = ref<string | null>(null);
const to = ref<string | null>(null);
watch([from, to], () => {
    filters.value = {
        ...filters.value,
        from: from.value ? from.value.slice(0, 10) : undefined,
        to: to.value ? to.value.slice(0, 10) : undefined,
    };
});

watch(filters, () => (page.value = 1));

const hasFilters = computed(
    () =>
        modelFilter.value !== 'all' ||
        eventFilter.value !== 'all' ||
        actorFilter.value !== 'all' ||
        !!from.value ||
        !!to.value
);

function resetFilters() {
    modelFilter.value = 'all';
    eventFilter.value = 'all';
    actorFilter.value = 'all';
    from.value = null;
    to.value = null;
}

const selectedChanges = computed(() =>
    selectedAudit.value ? auditChanges(selectedAudit.value) : []
);

function modelLabel(audit: Audit) {
    return audit.auditable_label || audit.auditable_type;
}

function auditSummary(audit: Audit) {
    const [first, ...rest] = auditChanges(audit);
    if (!first) {
        return 'No field changes';
    }
    const label = auditFieldLabel(first.field);
    return rest.length === 0 ? label : `${label} +${rest.length} more`;
}
</script>

<template>
    <AppLayout title="Audit log" data-testid="audit_log_view">
        <MainContainer
            class="py-5 border-b border-default-background-separator flex flex-wrap gap-3 justify-between items-center">
            <PageTitle :icon="ShieldCheckIcon" title="Audit log"></PageTitle>
            <div class="flex flex-wrap items-center gap-2">
                <Select v-model="modelFilter">
                    <SelectTrigger size="sm">
                        <SelectValue>
                            {{
                                modelFilter === 'all'
                                    ? 'All models'
                                    : auditModelOptions.find(
                                          (option) => option.value === modelFilter
                                      )?.label
                            }}
                        </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                        <SelectItem value="all">All models</SelectItem>
                        <SelectItem
                            v-for="option in auditModelOptions"
                            :key="option.value"
                            :value="option.value">
                            {{ option.label }}
                        </SelectItem>
                    </SelectContent>
                </Select>
                <Select v-model="eventFilter">
                    <SelectTrigger size="sm">
                        <SelectValue>
                            {{
                                eventFilter === 'all'
                                    ? 'All events'
                                    : auditEventOptions.find(
                                          (option) => option.value === eventFilter
                                      )?.label
                            }}
                        </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                        <SelectItem value="all">All events</SelectItem>
                        <SelectItem
                            v-for="option in auditEventOptions"
                            :key="option.value"
                            :value="option.value">
                            {{ option.label }}
                        </SelectItem>
                    </SelectContent>
                </Select>
                <Select v-model="actorFilter">
                    <SelectTrigger size="sm">
                        <SelectValue>
                            {{
                                actorFilter === 'all'
                                    ? 'All users'
                                    : actorOptions.find((option) => option.value === actorFilter)
                                          ?.label
                            }}
                        </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                        <SelectItem value="all">All users</SelectItem>
                        <SelectItem
                            v-for="option in actorOptions"
                            :key="option.value"
                            :value="option.value">
                            {{ option.label }}
                        </SelectItem>
                    </SelectContent>
                </Select>
                <div class="w-36">
                    <DatePicker v-model="from" clearable size="sm" aria-label="From"></DatePicker>
                </div>
                <div class="w-36">
                    <DatePicker v-model="to" clearable size="sm" aria-label="To"></DatePicker>
                </div>
                <SecondaryButton v-if="hasFilters" :icon="ArrowPathIcon" @click="resetFilters">
                    Reset
                </SecondaryButton>
            </div>
        </MainContainer>

        <div class="flow-root">
            <div class="inline-block min-w-full align-middle">
                <div
                    data-testid="audit_log_table"
                    class="grid min-w-full"
                    style="grid-template-columns: 150px 180px 120px 160px 1fr">
                    <TableHeading>
                        <div
                            class="py-3.5 pl-4 pr-3 text-left text-xs text-text-tertiary sm:pl-6 lg:pl-8 3xl:pl-12">
                            Time
                        </div>
                        <div class="px-3 py-3.5 text-left text-xs text-text-tertiary">User</div>
                        <div class="px-3 py-3.5 text-left text-xs text-text-tertiary">Event</div>
                        <div class="px-3 py-3.5 text-left text-xs text-text-tertiary">Model</div>
                        <div class="px-3 py-3.5 text-left text-xs text-text-tertiary">Changes</div>
                    </TableHeading>

                    <template v-for="audit in audits" :key="audit.id">
                        <TableRow
                            class="[&>*]:cursor-pointer [&>*]:hover:bg-white/5"
                            @click="selectedAudit = audit">
                            <div
                                class="whitespace-nowrap py-4 pl-4 pr-3 text-sm text-text-primary sm:pl-6 lg:pl-8 3xl:pl-12">
                                {{ formatAuditTimestamp(audit.created_at) }}
                            </div>
                            <div class="whitespace-nowrap px-3 py-4 text-sm text-text-secondary">
                                {{ audit.actor_name || audit.actor_id || '—' }}
                            </div>
                            <div class="whitespace-nowrap px-3 py-4 text-sm">
                                <span class="inline-flex items-center gap-2 text-text-secondary">
                                    <span
                                        class="h-2 w-2 rounded-full"
                                        :class="auditEventDotClass(audit.event)"></span>
                                    {{ audit.event }}
                                </span>
                            </div>
                            <div class="whitespace-nowrap px-3 py-4 text-sm text-text-primary">
                                {{ modelLabel(audit) }}
                            </div>
                            <div class="px-3 py-4 text-sm text-text-secondary">
                                {{ auditSummary(audit) }}
                            </div>
                        </TableRow>
                    </template>

                    <div
                        v-if="!isLoading && audits.length === 0"
                        class="col-span-5 py-24 text-center">
                        <ShieldCheckIcon
                            class="w-8 text-icon-default inline pb-2"></ShieldCheckIcon>
                        <h3 class="text-text-primary font-semibold">No audit entries found</h3>
                        <p class="text-text-secondary">
                            Changes to projects, time entries and other records show up here.
                        </p>
                    </div>
                </div>
            </div>
        </div>

        <MainContainer v-if="total > 15" class="py-4 flex justify-center">
            <Pagination v-model:page="page" :total="total"></Pagination>
        </MainContainer>

        <Modal :show="!!selectedAudit" max-width="2xl" @close="selectedAudit = null">
            <div v-if="selectedAudit" class="p-6 space-y-5">
                <div class="flex items-start justify-between gap-4">
                    <div>
                        <h2 class="text-lg font-semibold text-text-primary">
                            {{ modelLabel(selectedAudit) }} {{ selectedAudit.event }}
                        </h2>
                        <p class="text-sm text-text-secondary">
                            {{ formatAuditTimestamp(selectedAudit.created_at) }} by
                            {{ selectedAudit.actor_name || selectedAudit.actor_id || 'system' }}
                        </p>
                    </div>
                    <Badge>
                        <span
                            class="h-2 w-2 rounded-full"
                            :class="auditEventDotClass(selectedAudit.event)"></span>
                        <span>{{ selectedAudit.event }}</span>
                    </Badge>
                </div>

                <div class="overflow-hidden rounded-md border border-default-background-separator">
                    <table class="min-w-full divide-y divide-row-separator text-sm">
                        <thead class="bg-row-heading-background text-xs text-text-tertiary">
                            <tr>
                                <th class="px-3 py-2 text-left font-medium">Field</th>
                                <th class="px-3 py-2 text-left font-medium">Old value</th>
                                <th class="px-3 py-2 text-left font-medium">New value</th>
                            </tr>
                        </thead>
                        <tbody class="divide-y divide-row-separator">
                            <tr v-for="change in selectedChanges" :key="change.field">
                                <td class="px-3 py-2 font-medium text-text-primary">
                                    {{ auditFieldLabel(change.field) }}
                                </td>
                                <td class="px-3 py-2 text-text-secondary break-all">
                                    {{ change.oldValue }}
                                </td>
                                <td class="px-3 py-2 text-text-primary break-all">
                                    {{ change.newValue }}
                                </td>
                            </tr>
                            <tr v-if="selectedChanges.length === 0">
                                <td class="px-3 py-2 text-text-secondary" colspan="3">
                                    No field changes recorded.
                                </td>
                            </tr>
                        </tbody>
                    </table>
                </div>

                <dl class="grid grid-cols-2 gap-3 text-xs text-text-secondary">
                    <div>
                        <dt class="text-text-tertiary">Model id</dt>
                        <dd class="break-all">{{ selectedAudit.auditable_id }}</dd>
                    </div>
                    <div>
                        <dt class="text-text-tertiary">IP address</dt>
                        <dd>{{ selectedAudit.ip_address || '—' }}</dd>
                    </div>
                    <div class="col-span-2">
                        <dt class="text-text-tertiary">Request</dt>
                        <dd class="break-all">{{ selectedAudit.url || '—' }}</dd>
                    </div>
                    <div class="col-span-2">
                        <dt class="text-text-tertiary">User agent</dt>
                        <dd class="break-all">{{ selectedAudit.user_agent || '—' }}</dd>
                    </div>
                </dl>
            </div>
            <template #footer>
                <SecondaryButton @click="selectedAudit = null">Close</SecondaryButton>
            </template>
        </Modal>
    </AppLayout>
</template>
