<script setup lang="ts">
import AppLayout from '@/Layouts/AppLayout.vue';
import MainContainer from '@/packages/ui/src/MainContainer.vue';
import PageTitle from '@/Components/Common/PageTitle.vue';
import PrimaryButton from '@/packages/ui/src/Buttons/PrimaryButton.vue';
import SecondaryButton from '@/packages/ui/src/Buttons/SecondaryButton.vue';
import DangerButton from '@/packages/ui/src/Buttons/DangerButton.vue';
import TableHeading from '@/Components/Common/TableHeading.vue';
import TableRow from '@/Components/TableRow.vue';
import Modal from '@/packages/ui/src/Modal.vue';
import Pagination from '@/packages/ui/src/Pagination.vue';
import Badge from '@/packages/ui/src/Badge.vue';
import TextInput from '@/packages/ui/src/Input/TextInput.vue';
import TextareaInput from '@/packages/ui/src/Input/TextareaInput.vue';
import InputLabel from '@/packages/ui/src/Input/InputLabel.vue';
import Checkbox from '@/packages/ui/src/Input/Checkbox.vue';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/packages/ui/src';
import { BanknotesIcon, DocumentDuplicateIcon, PlusIcon, TrashIcon } from '@heroicons/vue/20/solid';
import { computed, reactive, ref, watch } from 'vue';
import { useQueryClient } from '@tanstack/vue-query';
import { api } from '@/packages/api/src';
import type {
    CreateInvoiceBody,
    DetailedInvoice,
    InvoiceIndexEntry,
    InvoiceRecipient,
    InvoiceRecipientBody,
    TimeEntry,
    UpdateInvoiceBody,
    UpdateInvoiceSettings,
} from '@/packages/api/src';
import {
    useInvoiceRecipientsQuery,
    useInvoiceSettingsQuery,
    useInvoicesQuery,
} from '@/utils/useInvoicesQuery';
import { getCurrentOrganizationId } from '@/utils/useUser';
import { formatCents } from '@/packages/ui/src/utils/money';
import {
    canCreateInvoices,
    canDeleteInvoices,
    canDownloadInvoices,
    canUpdateInvoices,
} from '@/utils/permissions';

type EntryForm = {
    id: string | null;
    name: string;
    description: string;
    price: string;
    qty: string;
};

const queryClient = useQueryClient();
const page = ref(1);
const statusFilter = ref('all');
const status = computed(() => (statusFilter.value === 'all' ? undefined : statusFilter.value));
const { invoices, total, isLoading } = useInvoicesQuery(status, page);
const { recipients } = useInvoiceRecipientsQuery();
const { settings } = useInvoiceSettingsQuery();
watch(statusFilter, () => (page.value = 1));

function invalidate() {
    const org = getCurrentOrganizationId();
    queryClient.invalidateQueries({ queryKey: ['invoices', org] });
    queryClient.invalidateQueries({ queryKey: ['invoice-recipients', org] });
    queryClient.invalidateQueries({ queryKey: ['invoice-settings', org] });
}

const statusBadgeClass = (s: string) =>
    ({
        draft: 'bg-gray-500/20 text-gray-300',
        sent: 'bg-blue-500/20 text-blue-300',
        paid: 'bg-green-500/20 text-green-300',
        cancelled: 'bg-red-500/20 text-red-300',
    })[s] ?? 'bg-gray-500/20 text-gray-300';

// ---- invoice create / edit ----

const showForm = ref(false);
const editing = ref<DetailedInvoice | null>(null);
const saving = ref(false);
const formErrors = ref<Record<string, string[]>>({});

const emptyForm = () => ({
    invoice_recipient_id: '',
    reference: '',
    seller_name: '',
    seller_vatin: '',
    seller_address_line_1: '',
    seller_address_line_2: '',
    seller_address_line_3: '',
    seller_address_post_code: '',
    seller_address_city: '',
    seller_address_country: '',
    seller_phone: '',
    seller_email: '',
    date: new Date().toISOString().slice(0, 10),
    due_at: '',
    paid_date: '',
    status: 'draft',
    currency: 'EUR',
    payment_iban: '',
    tax_rate: '0',
    discount_amount: '0',
    discount_type: 'percentage' as 'percentage' | 'fixed',
    is_eu_reverse_charge: false,
    billing_period_start: '',
    billing_period_end: '',
    footer: '',
    notes: '',
    payment_terms: '',
});
const form = reactive(emptyForm());
const entries = ref<EntryForm[]>([]);

const orNull = (v: string) => (v === '' ? null : v);
const toMinor = (v: string) => Math.round(parseFloat(v || '0') * 100);
const toMajor = (minor: number) => (minor / 100).toFixed(2);

const entriesTotal = computed(() =>
    entries.value.reduce((sum, e) => sum + toMinor(e.price) * (parseFloat(e.qty) || 0), 0)
);
const invoiceTotal = computed(() => {
    const discount =
        form.discount_type === 'percentage'
            ? Math.round(entriesTotal.value * (parseFloat(form.discount_amount) / 100))
            : toMinor(form.discount_amount);
    const taxable = Math.max(0, entriesTotal.value - discount);
    const tax = form.is_eu_reverse_charge
        ? 0
        : Math.round(taxable * (parseFloat(form.tax_rate) / 100));
    return taxable + tax;
});

function addEntry() {
    entries.value.push({ id: null, name: '', description: '', price: '0.00', qty: '1' });
}
function removeEntry(i: number) {
    entries.value.splice(i, 1);
}

function openCreate() {
    editing.value = null;
    Object.assign(form, emptyForm());
    const s = settings.value;
    if (s) {
        form.seller_name = s.seller_name ?? '';
        form.seller_vatin = s.seller_vatin ?? '';
        form.seller_address_line_1 = s.seller_address_line_1 ?? '';
        form.seller_address_line_2 = s.seller_address_line_2 ?? '';
        form.seller_address_line_3 = s.seller_address_line_3 ?? '';
        form.seller_address_post_code = s.seller_address_post_code ?? '';
        form.seller_address_city = s.seller_address_city ?? '';
        form.seller_address_country = s.seller_address_country ?? '';
        form.seller_phone = s.seller_phone ?? '';
        form.seller_email = s.seller_email ?? '';
        form.footer = s.footer_default ?? '';
        form.notes = s.notes_default ?? '';
        form.tax_rate = String(s.tax_rate_default ?? 0);
    }
    entries.value = [{ id: null, name: '', description: '', price: '0.00', qty: '1' }];
    formErrors.value = {};
    showForm.value = true;
}

async function openEdit(inv: InvoiceIndexEntry) {
    const org = getCurrentOrganizationId();
    if (!org) return;
    const response = await api.getInvoice({
        params: { organization: org, invoice: inv.id },
    });
    const detail = response.data;
    editing.value = detail;
    Object.assign(form, emptyForm(), {
        invoice_recipient_id: detail.invoice_recipient_id,
        reference: detail.reference,
        seller_name: detail.seller_name ?? '',
        seller_vatin: detail.seller_vatin ?? '',
        seller_address_line_1: detail.seller_address_line_1 ?? '',
        seller_address_line_2: detail.seller_address_line_2 ?? '',
        seller_address_line_3: detail.seller_address_line_3 ?? '',
        seller_address_post_code: detail.seller_address_post_code ?? '',
        seller_address_city: detail.seller_address_city ?? '',
        seller_address_country: detail.seller_address_country ?? '',
        seller_phone: detail.seller_phone ?? '',
        seller_email: detail.seller_email ?? '',
        date: detail.date,
        due_at: detail.due_at ?? '',
        paid_date: detail.paid_date ?? '',
        status: detail.status,
        currency: detail.currency,
        payment_iban: detail.payment_iban ?? '',
        tax_rate: String(detail.tax_rate),
        discount_amount: toMajor(detail.discount_amount),
        discount_type: detail.discount_type as 'percentage' | 'fixed',
        is_eu_reverse_charge: !!detail.is_eu_reverse_charge,
        billing_period_start: detail.billing_period_start ?? '',
        billing_period_end: detail.billing_period_end ?? '',
        footer: detail.footer ?? '',
        notes: detail.notes ?? '',
        payment_terms: detail.payment_terms ?? '',
    });
    entries.value = detail.entries.map((e) => ({
        id: e.id,
        name: e.name,
        description: e.description ?? '',
        price: toMajor(e.unit_price),
        qty: String(e.quantity),
    }));
    formErrors.value = {};
    showForm.value = true;
}

function entriesPayload() {
    return entries.value.map((e) => ({
        ...(e.id ? { id: e.id } : {}),
        name: e.name,
        description: orNull(e.description),
        unit_price: toMinor(e.price),
        quantity: parseFloat(e.qty) || 0,
    }));
}

function commonPayload() {
    return {
        invoice_recipient_id: form.invoice_recipient_id,
        reference: form.reference,
        seller_name: form.seller_name,
        seller_vatin: orNull(form.seller_vatin),
        seller_address_line_1: orNull(form.seller_address_line_1),
        seller_address_line_2: orNull(form.seller_address_line_2),
        seller_address_line_3: orNull(form.seller_address_line_3),
        seller_address_post_code: orNull(form.seller_address_post_code),
        seller_address_city: orNull(form.seller_address_city),
        seller_address_country: orNull(form.seller_address_country),
        seller_phone: orNull(form.seller_phone),
        seller_email: orNull(form.seller_email),
        date: form.date,
        due_at: orNull(form.due_at),
        paid_date: orNull(form.paid_date),
        currency: form.currency,
        payment_iban: orNull(form.payment_iban),
        tax_rate: parseInt(form.tax_rate) || 0,
        discount_amount: toMinor(form.discount_amount),
        discount_type: form.discount_type,
        is_eu_reverse_charge: form.is_eu_reverse_charge,
        billing_period_start: orNull(form.billing_period_start),
        billing_period_end: orNull(form.billing_period_end),
        footer: orNull(form.footer),
        notes: orNull(form.notes),
        payment_terms: orNull(form.payment_terms),
    };
}

async function saveInvoice() {
    const org = getCurrentOrganizationId();
    if (!org) return;
    saving.value = true;
    formErrors.value = {};
    try {
        if (editing.value) {
            const body = {
                ...commonPayload(),
                status: form.status as 'draft' | 'sent' | 'paid' | 'cancelled',
                entries: entriesPayload(),
            } as UpdateInvoiceBody;
            await api.updateInvoice(body, {
                params: { organization: org, invoice: editing.value.id },
            });
        } else {
            const body = {
                ...commonPayload(),
                entries: entriesPayload(),
            } as CreateInvoiceBody;
            await api.createInvoice(body, { params: { organization: org } });
        }
        showForm.value = false;
        invalidate();
    } catch (error: unknown) {
        const err = error as { data?: { errors?: Record<string, string[]> } };
        formErrors.value = err?.data?.errors ?? { general: ['Save failed.'] };
    } finally {
        saving.value = false;
    }
}

async function removeInvoice(inv: InvoiceIndexEntry) {
    const org = getCurrentOrganizationId();
    if (!org) return;
    await api.deleteInvoice(undefined, {
        params: { organization: org, invoice: inv.id },
    });
    invalidate();
}

async function downloadInvoice(inv: InvoiceIndexEntry, eInvoice: boolean) {
    const org = getCurrentOrganizationId();
    if (!org) return;
    const response = eInvoice
        ? await api.downloadEInvoice(undefined, {
              params: { organization: org, invoice: inv.id },
          })
        : await api.downloadInvoice(
              { with_e_invoice: false },
              { params: { organization: org, invoice: inv.id } }
          );
    const link = (response as { download_link?: string }).download_link;
    if (link) window.open(link, '_blank');
}

const copyTarget = ref<InvoiceIndexEntry | null>(null);
const copyReference = ref('');
async function saveCopy() {
    const org = getCurrentOrganizationId();
    if (!org || !copyTarget.value) return;
    await api.copyInvoice(
        { reference: copyReference.value },
        { params: { organization: org, invoice: copyTarget.value.id } }
    );
    copyTarget.value = null;
    copyReference.value = '';
    invalidate();
}

// ---- recipients ----

const showRecipients = ref(false);
const editingRecipient = ref<InvoiceRecipient | null>(null);
const recipientForm = reactive({
    name: '',
    vatin: '',
    address_line_1: '',
    address_line_2: '',
    address_line_3: '',
    address_post_code: '',
    address_city: '',
    address_country: '',
    phone: '',
    email: '',
    is_archived: false,
});

function openRecipient(r: InvoiceRecipient | null) {
    editingRecipient.value = r;
    Object.assign(recipientForm, {
        name: r?.name ?? '',
        vatin: r?.vatin ?? '',
        address_line_1: r?.address_line_1 ?? '',
        address_line_2: r?.address_line_2 ?? '',
        address_line_3: r?.address_line_3 ?? '',
        address_post_code: r?.address_post_code ?? '',
        address_city: r?.address_city ?? '',
        address_country: r?.address_country ?? '',
        phone: r?.phone ?? '',
        email: r?.email ?? '',
        is_archived: r?.is_archived ?? false,
    });
}

function recipientPayload(): InvoiceRecipientBody {
    return {
        name: recipientForm.name,
        vatin: orNull(recipientForm.vatin),
        address_line_1: orNull(recipientForm.address_line_1),
        address_line_2: orNull(recipientForm.address_line_2),
        address_line_3: orNull(recipientForm.address_line_3),
        address_post_code: orNull(recipientForm.address_post_code),
        address_city: orNull(recipientForm.address_city),
        address_country: orNull(recipientForm.address_country),
        phone: orNull(recipientForm.phone),
        email: orNull(recipientForm.email),
        is_archived: recipientForm.is_archived,
    };
}

async function saveRecipient() {
    const org = getCurrentOrganizationId();
    if (!org) return;
    if (editingRecipient.value) {
        await api.updateInvoiceRecipient(recipientPayload(), {
            params: { organization: org, invoiceRecipient: editingRecipient.value.id },
        });
    } else {
        await api.createInvoiceRecipient(recipientPayload(), {
            params: { organization: org },
        });
    }
    editingRecipient.value = null;
    openRecipient(null);
    invalidate();
}

async function removeRecipient(r: InvoiceRecipient) {
    const org = getCurrentOrganizationId();
    if (!org) return;
    await api.deleteInvoiceRecipient(undefined, {
        params: { organization: org, invoiceRecipient: r.id },
    });
    invalidate();
}

// ---- settings ----

const showSettings = ref(false);
const settingsForm = reactive({
    seller_name: '',
    seller_vatin: '',
    seller_address_line_1: '',
    seller_address_line_2: '',
    seller_address_line_3: '',
    seller_address_post_code: '',
    seller_address_city: '',
    seller_address_country: '',
    seller_phone: '',
    seller_email: '',
    footer_default: '',
    notes_default: '',
    tax_rate_default: '',
    e_invoicing_enabled: false,
});

function openSettings() {
    const s = settings.value;
    Object.assign(settingsForm, {
        seller_name: s?.seller_name ?? '',
        seller_vatin: s?.seller_vatin ?? '',
        seller_address_line_1: s?.seller_address_line_1 ?? '',
        seller_address_line_2: s?.seller_address_line_2 ?? '',
        seller_address_line_3: s?.seller_address_line_3 ?? '',
        seller_address_post_code: s?.seller_address_post_code ?? '',
        seller_address_city: s?.seller_address_city ?? '',
        seller_address_country: s?.seller_address_country ?? '',
        seller_phone: s?.seller_phone ?? '',
        seller_email: s?.seller_email ?? '',
        footer_default: s?.footer_default ?? '',
        notes_default: s?.notes_default ?? '',
        tax_rate_default: s?.tax_rate_default != null ? String(s.tax_rate_default) : '',
        e_invoicing_enabled: s?.e_invoicing_enabled ?? false,
    });
    showSettings.value = true;
}

async function saveSettings() {
    const org = getCurrentOrganizationId();
    if (!org) return;
    const body: UpdateInvoiceSettings = {
        seller_name: orNull(settingsForm.seller_name),
        seller_vatin: orNull(settingsForm.seller_vatin),
        seller_address_line_1: orNull(settingsForm.seller_address_line_1),
        seller_address_line_2: orNull(settingsForm.seller_address_line_2),
        seller_address_line_3: orNull(settingsForm.seller_address_line_3),
        seller_address_post_code: orNull(settingsForm.seller_address_post_code),
        seller_address_city: orNull(settingsForm.seller_address_city),
        seller_address_country: orNull(settingsForm.seller_address_country),
        seller_phone: orNull(settingsForm.seller_phone),
        seller_email: orNull(settingsForm.seller_email),
        footer_default: orNull(settingsForm.footer_default),
        notes_default: orNull(settingsForm.notes_default),
        tax_rate_default:
            settingsForm.tax_rate_default === '' ? null : parseFloat(settingsForm.tax_rate_default),
        e_invoicing_enabled: settingsForm.e_invoicing_enabled,
    };
    await api.updateInvoiceSettings(body, { params: { organization: org } });
    showSettings.value = false;
    invalidate();
}

// ---- add lines from billable time entries ----

const showTimePicker = ref(false);
const billableEntries = ref<TimeEntry[]>([]);
const selectedEntryIds = ref<Set<string>>(new Set());
const loadingEntries = ref(false);

async function openTimePicker() {
    const org = getCurrentOrganizationId();
    if (!org) return;
    showTimePicker.value = true;
    loadingEntries.value = true;
    try {
        const response = await api.getTimeEntries({
            params: { organization: org },
            queries: { billable: 'true', limit: 100 },
        });
        billableEntries.value = response.data.filter((e) => e.end);
    } finally {
        loadingEntries.value = false;
    }
}

function toggleEntry(id: string) {
    if (selectedEntryIds.value.has(id)) selectedEntryIds.value.delete(id);
    else selectedEntryIds.value.add(id);
    selectedEntryIds.value = new Set(selectedEntryIds.value);
}

function entryHours(e: TimeEntry): number {
    const start = new Date(e.start).getTime();
    const end = new Date(e.end as string).getTime();
    return Math.max(0, (end - start) / 3600000);
}

function addSelectedEntries() {
    for (const e of billableEntries.value) {
        if (!selectedEntryIds.value.has(e.id)) continue;
        entries.value.push({
            id: null,
            name: e.description || 'Time entry',
            description: e.start.slice(0, 10),
            price: toMajor(Number(e.billable_rate ?? 0)),
            qty: entryHours(e).toFixed(2),
        });
    }
    showTimePicker.value = false;
    selectedEntryIds.value = new Set();
}

const firstError = computed(() => {
    const keys = Object.keys(formErrors.value);
    const first = keys.length ? formErrors.value[keys[0] as string] : undefined;
    return first ? first[0] : null;
});
</script>

<template>
    <AppLayout title="Invoices" data-testid="invoices_view">
        <MainContainer
            class="py-5 border-b border-default-background-separator flex flex-wrap gap-3 justify-between items-center">
            <PageTitle :icon="BanknotesIcon" title="Invoices"></PageTitle>
            <div class="flex flex-wrap items-center gap-2">
                <Select v-model="statusFilter">
                    <SelectTrigger size="sm">
                        <SelectValue>
                            {{ statusFilter === 'all' ? 'All statuses' : statusFilter }}
                        </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                        <SelectItem value="all">All statuses</SelectItem>
                        <SelectItem value="draft">Draft</SelectItem>
                        <SelectItem value="sent">Sent</SelectItem>
                        <SelectItem value="paid">Paid</SelectItem>
                        <SelectItem value="cancelled">Cancelled</SelectItem>
                    </SelectContent>
                </Select>
                <SecondaryButton @click="showRecipients = true">Recipients</SecondaryButton>
                <SecondaryButton @click="openSettings">Settings</SecondaryButton>
                <PrimaryButton v-if="canCreateInvoices()" :icon="PlusIcon" @click="openCreate">
                    New invoice
                </PrimaryButton>
            </div>
        </MainContainer>

        <div v-if="invoices.length > 0" class="flow-root">
            <div class="inline-block min-w-full align-middle">
                <div
                    data-testid="invoices_table"
                    class="grid min-w-full"
                    style="grid-template-columns: 140px 1fr 130px 130px 110px 280px">
                    <TableHeading>
                        <div
                            class="py-3.5 pl-4 pr-3 text-left text-xs text-text-tertiary sm:pl-6 lg:pl-8">
                            Reference
                        </div>
                        <div class="px-3 py-3.5 text-left text-xs text-text-tertiary">
                            Recipient
                        </div>
                        <div class="px-3 py-3.5 text-left text-xs text-text-tertiary">Date</div>
                        <div class="px-3 py-3.5 text-left text-xs text-text-tertiary">Due</div>
                        <div class="px-3 py-3.5 text-left text-xs text-text-tertiary">Status</div>
                        <div class="px-3 py-3.5 text-left text-xs text-text-tertiary">Actions</div>
                    </TableHeading>

                    <template v-for="invoice in invoices" :key="invoice.id">
                        <TableRow>
                            <div
                                class="whitespace-nowrap py-4 pl-4 pr-3 text-sm text-text-primary sm:pl-6 lg:pl-8">
                                {{ invoice.reference }}
                            </div>
                            <div class="px-3 py-4 text-sm text-text-secondary">
                                {{ invoice.recipient }}
                            </div>
                            <div class="whitespace-nowrap px-3 py-4 text-sm text-text-secondary">
                                {{ invoice.date }}
                            </div>
                            <div class="whitespace-nowrap px-3 py-4 text-sm text-text-secondary">
                                {{ invoice.due_at || '—' }}
                            </div>
                            <div class="px-3 py-4 text-sm">
                                <Badge>
                                    <span
                                        :class="statusBadgeClass(invoice.status)"
                                        class="rounded px-1.5 py-0.5 text-xs">
                                        {{ invoice.status_label || invoice.status }}
                                    </span>
                                </Badge>
                            </div>
                            <div class="px-3 py-3 text-sm flex items-center gap-1">
                                <SecondaryButton
                                    v-if="canUpdateInvoices()"
                                    class="!px-2"
                                    @click="openEdit(invoice)">
                                    Edit
                                </SecondaryButton>
                                <SecondaryButton
                                    v-if="canDownloadInvoices()"
                                    class="!px-2"
                                    @click="downloadInvoice(invoice, false)">
                                    PDF
                                </SecondaryButton>
                                <SecondaryButton
                                    v-if="canDownloadInvoices()"
                                    class="!px-2"
                                    @click="downloadInvoice(invoice, true)">
                                    XML
                                </SecondaryButton>
                                <SecondaryButton
                                    v-if="canCreateInvoices()"
                                    class="!px-2"
                                    :icon="DocumentDuplicateIcon"
                                    @click="
                                        copyTarget = invoice;
                                        copyReference = invoice.reference + '-copy';
                                    ">
                                </SecondaryButton>
                                <DangerButton
                                    v-if="canDeleteInvoices() && invoice.status === 'draft'"
                                    class="!px-2"
                                    :icon="TrashIcon"
                                    @click="removeInvoice(invoice)">
                                </DangerButton>
                            </div>
                        </TableRow>
                    </template>
                </div>
            </div>
        </div>

        <MainContainer
            v-else-if="isLoading"
            role="status"
            aria-live="polite"
            class="py-24 text-center text-text-secondary">
            Loading invoices…
        </MainContainer>
        <MainContainer v-else data-testid="invoice_empty_state" class="py-24 text-center">
            <BanknotesIcon
                class="w-8 text-icon-default inline pb-2"
                aria-hidden="true"></BanknotesIcon>
            <h3 class="text-text-primary font-semibold">No invoices yet</h3>
            <p class="text-text-secondary">Create your first invoice to bill tracked time.</p>
        </MainContainer>

        <MainContainer v-if="total > 15" class="py-4 flex justify-center">
            <Pagination v-model:page="page" :total="total"></Pagination>
        </MainContainer>

        <!-- create / edit invoice -->
        <Modal :show="showForm" max-width="4xl" @close="showForm = false">
            <div class="p-6 space-y-5 max-h-[80vh] overflow-y-auto">
                <h2 class="text-lg font-semibold text-text-primary">
                    {{ editing ? 'Edit invoice' : 'New invoice' }}
                </h2>
                <p v-if="firstError" class="text-sm text-red-400">{{ firstError }}</p>

                <div class="grid grid-cols-2 gap-3">
                    <div>
                        <InputLabel value="Recipient" />
                        <Select v-model="form.invoice_recipient_id">
                            <SelectTrigger class="w-full">
                                <SelectValue placeholder="Select recipient" />
                            </SelectTrigger>
                            <SelectContent>
                                <SelectItem v-for="r in recipients" :key="r.id" :value="r.id">
                                    {{ r.name }}
                                </SelectItem>
                            </SelectContent>
                        </Select>
                    </div>
                    <div>
                        <InputLabel value="Reference" />
                        <TextInput v-model="form.reference" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Date" />
                        <TextInput v-model="form.date" type="date" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Due date" />
                        <TextInput v-model="form.due_at" type="date" class="w-full" />
                    </div>
                    <div v-if="editing">
                        <InputLabel value="Paid date" />
                        <TextInput v-model="form.paid_date" type="date" class="w-full" />
                    </div>
                    <div v-if="editing">
                        <InputLabel value="Status" />
                        <Select v-model="form.status">
                            <SelectTrigger class="w-full"><SelectValue /></SelectTrigger>
                            <SelectContent>
                                <SelectItem value="draft">Draft</SelectItem>
                                <SelectItem value="sent">Sent</SelectItem>
                                <SelectItem value="paid">Paid</SelectItem>
                                <SelectItem value="cancelled">Cancelled</SelectItem>
                            </SelectContent>
                        </Select>
                    </div>
                    <div>
                        <InputLabel value="Currency" />
                        <TextInput v-model="form.currency" class="w-full" maxlength="3" />
                    </div>
                    <div>
                        <InputLabel value="Payment IBAN" />
                        <TextInput v-model="form.payment_iban" class="w-full" />
                    </div>
                </div>

                <div class="grid grid-cols-3 gap-3">
                    <div>
                        <InputLabel value="Tax rate %" />
                        <TextInput v-model="form.tax_rate" type="number" min="0" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Discount" />
                        <TextInput
                            v-model="form.discount_amount"
                            type="number"
                            min="0"
                            step="0.01"
                            class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Discount type" />
                        <Select v-model="form.discount_type">
                            <SelectTrigger class="w-full"><SelectValue /></SelectTrigger>
                            <SelectContent>
                                <SelectItem value="percentage">Percentage</SelectItem>
                                <SelectItem value="fixed">Fixed</SelectItem>
                            </SelectContent>
                        </Select>
                    </div>
                </div>
                <label class="flex items-center gap-2 text-sm text-text-secondary">
                    <Checkbox v-model:checked="form.is_eu_reverse_charge" />
                    EU reverse charge (no VAT charged)
                </label>

                <div>
                    <div class="flex items-center justify-between mb-2">
                        <InputLabel value="Line items" />
                        <div class="flex gap-2">
                            <SecondaryButton class="!px-2" @click="openTimePicker">
                                From billable time
                            </SecondaryButton>
                            <SecondaryButton class="!px-2" :icon="PlusIcon" @click="addEntry">
                                Add line
                            </SecondaryButton>
                        </div>
                    </div>
                    <div class="space-y-2">
                        <div
                            v-for="(entry, i) in entries"
                            :key="i"
                            class="grid grid-cols-[1fr_1fr_110px_90px_36px] gap-2 items-center">
                            <TextInput v-model="entry.name" placeholder="Name" />
                            <TextInput v-model="entry.description" placeholder="Description" />
                            <TextInput
                                v-model="entry.price"
                                type="number"
                                min="0"
                                step="0.01"
                                placeholder="Price" />
                            <TextInput
                                v-model="entry.qty"
                                type="number"
                                min="0"
                                step="0.01"
                                placeholder="Qty" />
                            <DangerButton class="!px-2" :icon="TrashIcon" @click="removeEntry(i)" />
                        </div>
                    </div>
                    <div class="mt-3 text-right text-sm text-text-primary">
                        Total: {{ formatCents(invoiceTotal, form.currency) }}
                    </div>
                </div>

                <details class="text-sm">
                    <summary class="cursor-pointer text-text-secondary">Seller details</summary>
                    <div class="grid grid-cols-2 gap-3 mt-3">
                        <div>
                            <InputLabel value="Seller name" />
                            <TextInput v-model="form.seller_name" class="w-full" />
                        </div>
                        <div>
                            <InputLabel value="VAT ID" />
                            <TextInput v-model="form.seller_vatin" class="w-full" />
                        </div>
                        <div>
                            <InputLabel value="Address line 1" />
                            <TextInput v-model="form.seller_address_line_1" class="w-full" />
                        </div>
                        <div>
                            <InputLabel value="Address line 2" />
                            <TextInput v-model="form.seller_address_line_2" class="w-full" />
                        </div>
                        <div>
                            <InputLabel value="Address line 3" />
                            <TextInput v-model="form.seller_address_line_3" class="w-full" />
                        </div>
                        <div>
                            <InputLabel value="Post code" />
                            <TextInput v-model="form.seller_address_post_code" class="w-full" />
                        </div>
                        <div>
                            <InputLabel value="City" />
                            <TextInput v-model="form.seller_address_city" class="w-full" />
                        </div>
                        <div>
                            <InputLabel value="Country" />
                            <TextInput v-model="form.seller_address_country" class="w-full" />
                        </div>
                        <div>
                            <InputLabel value="Phone" />
                            <TextInput v-model="form.seller_phone" class="w-full" />
                        </div>
                        <div>
                            <InputLabel value="Email" />
                            <TextInput v-model="form.seller_email" class="w-full" />
                        </div>
                        <div>
                            <InputLabel value="Billing period start" />
                            <TextInput
                                v-model="form.billing_period_start"
                                type="date"
                                class="w-full" />
                        </div>
                        <div>
                            <InputLabel value="Billing period end" />
                            <TextInput
                                v-model="form.billing_period_end"
                                type="date"
                                class="w-full" />
                        </div>
                    </div>
                </details>

                <div class="grid grid-cols-1 gap-3">
                    <div>
                        <InputLabel value="Notes" />
                        <TextareaInput v-model="form.notes" class="w-full" :rows="2" />
                    </div>
                    <div>
                        <InputLabel value="Payment terms" />
                        <TextareaInput v-model="form.payment_terms" class="w-full" :rows="2" />
                    </div>
                    <div>
                        <InputLabel value="Footer" />
                        <TextareaInput v-model="form.footer" class="w-full" :rows="2" />
                    </div>
                </div>
            </div>
            <template #footer>
                <SecondaryButton @click="showForm = false">Cancel</SecondaryButton>
                <PrimaryButton :disabled="saving" @click="saveInvoice">
                    {{ editing ? 'Save' : 'Create' }}
                </PrimaryButton>
            </template>
        </Modal>

        <!-- duplicate -->
        <Modal :show="!!copyTarget" @close="copyTarget = null">
            <div class="p-6 space-y-4">
                <h2 class="text-lg font-semibold text-text-primary">Duplicate invoice</h2>
                <div>
                    <InputLabel value="New reference" />
                    <TextInput v-model="copyReference" class="w-full" />
                </div>
            </div>
            <template #footer>
                <SecondaryButton @click="copyTarget = null">Cancel</SecondaryButton>
                <PrimaryButton @click="saveCopy">Duplicate</PrimaryButton>
            </template>
        </Modal>

        <!-- recipients -->
        <Modal :show="showRecipients" max-width="3xl" @close="showRecipients = false">
            <div class="p-6 space-y-4 max-h-[80vh] overflow-y-auto">
                <h2 class="text-lg font-semibold text-text-primary">Invoice recipients</h2>
                <div class="divide-y divide-row-separator">
                    <div
                        v-for="r in recipients"
                        :key="r.id"
                        class="py-2 flex items-center justify-between">
                        <div class="text-sm text-text-primary">
                            {{ r.name }}
                            <span v-if="r.is_archived" class="text-xs text-text-tertiary">
                                (archived)
                            </span>
                        </div>
                        <div class="flex gap-2">
                            <SecondaryButton class="!px-2" @click="openRecipient(r)">
                                Edit
                            </SecondaryButton>
                            <DangerButton
                                v-if="!r.has_non_draft_invoices"
                                class="!px-2"
                                :icon="TrashIcon"
                                @click="removeRecipient(r)" />
                        </div>
                    </div>
                </div>
                <h3 class="text-sm font-semibold text-text-primary pt-2">
                    {{ editingRecipient ? 'Edit recipient' : 'New recipient' }}
                </h3>
                <div class="grid grid-cols-2 gap-3">
                    <div>
                        <InputLabel value="Name" />
                        <TextInput v-model="recipientForm.name" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="VAT ID" />
                        <TextInput v-model="recipientForm.vatin" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Address line 1" />
                        <TextInput v-model="recipientForm.address_line_1" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Address line 2" />
                        <TextInput v-model="recipientForm.address_line_2" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Post code" />
                        <TextInput v-model="recipientForm.address_post_code" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="City" />
                        <TextInput v-model="recipientForm.address_city" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Country" />
                        <TextInput v-model="recipientForm.address_country" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Phone" />
                        <TextInput v-model="recipientForm.phone" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Email" />
                        <TextInput v-model="recipientForm.email" class="w-full" />
                    </div>
                    <label class="flex items-center gap-2 text-sm text-text-secondary self-end">
                        <Checkbox v-model:checked="recipientForm.is_archived" />
                        Archived
                    </label>
                </div>
                <div class="flex gap-2">
                    <PrimaryButton @click="saveRecipient">
                        {{ editingRecipient ? 'Save recipient' : 'Add recipient' }}
                    </PrimaryButton>
                    <SecondaryButton v-if="editingRecipient" @click="openRecipient(null)">
                        Clear
                    </SecondaryButton>
                </div>
            </div>
            <template #footer>
                <SecondaryButton @click="showRecipients = false">Close</SecondaryButton>
            </template>
        </Modal>

        <!-- settings -->
        <Modal :show="showSettings" max-width="3xl" @close="showSettings = false">
            <div class="p-6 space-y-4 max-h-[80vh] overflow-y-auto">
                <h2 class="text-lg font-semibold text-text-primary">Invoice settings</h2>
                <div class="grid grid-cols-2 gap-3">
                    <div>
                        <InputLabel value="Seller name" />
                        <TextInput v-model="settingsForm.seller_name" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="VAT ID" />
                        <TextInput v-model="settingsForm.seller_vatin" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Address line 1" />
                        <TextInput v-model="settingsForm.seller_address_line_1" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Address line 2" />
                        <TextInput v-model="settingsForm.seller_address_line_2" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Post code" />
                        <TextInput v-model="settingsForm.seller_address_post_code" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="City" />
                        <TextInput v-model="settingsForm.seller_address_city" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Country" />
                        <TextInput v-model="settingsForm.seller_address_country" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Phone" />
                        <TextInput v-model="settingsForm.seller_phone" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Email" />
                        <TextInput v-model="settingsForm.seller_email" class="w-full" />
                    </div>
                    <div>
                        <InputLabel value="Default tax rate %" />
                        <TextInput
                            v-model="settingsForm.tax_rate_default"
                            type="number"
                            min="0"
                            class="w-full" />
                    </div>
                </div>
                <div>
                    <InputLabel value="Default notes" />
                    <TextareaInput v-model="settingsForm.notes_default" class="w-full" :rows="2" />
                </div>
                <div>
                    <InputLabel value="Default footer" />
                    <TextareaInput v-model="settingsForm.footer_default" class="w-full" :rows="2" />
                </div>
                <label class="flex items-center gap-2 text-sm text-text-secondary">
                    <Checkbox v-model:checked="settingsForm.e_invoicing_enabled" />
                    Enable e-invoice (UBL) generation
                </label>
            </div>
            <template #footer>
                <SecondaryButton @click="showSettings = false">Cancel</SecondaryButton>
                <PrimaryButton @click="saveSettings">Save</PrimaryButton>
            </template>
        </Modal>

        <!-- billable time picker -->
        <Modal :show="showTimePicker" max-width="3xl" @close="showTimePicker = false">
            <div class="p-6 space-y-4 max-h-[80vh] overflow-y-auto">
                <h2 class="text-lg font-semibold text-text-primary">Add billable time entries</h2>
                <p v-if="loadingEntries" class="text-sm text-text-secondary">Loading…</p>
                <div v-else class="divide-y divide-row-separator">
                    <label
                        v-for="e in billableEntries"
                        :key="e.id"
                        class="py-2 flex items-center gap-3 text-sm text-text-primary cursor-pointer">
                        <Checkbox
                            :checked="selectedEntryIds.has(e.id)"
                            @update:checked="toggleEntry(e.id)" />
                        <span class="flex-1">{{ e.description || '(no description)' }}</span>
                        <span class="text-text-tertiary text-xs">
                            {{ e.start.slice(0, 10) }} · {{ entryHours(e).toFixed(2) }}h ·
                            {{ formatCents(Number(e.billable_rate ?? 0), form.currency) }}/h
                        </span>
                    </label>
                    <p v-if="billableEntries.length === 0" class="text-sm text-text-secondary py-4">
                        No billable time entries found.
                    </p>
                </div>
            </div>
            <template #footer>
                <SecondaryButton @click="showTimePicker = false">Cancel</SecondaryButton>
                <PrimaryButton :disabled="selectedEntryIds.size === 0" @click="addSelectedEntries">
                    Add {{ selectedEntryIds.size }} line(s)
                </PrimaryButton>
            </template>
        </Modal>
    </AppLayout>
</template>
