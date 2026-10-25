import { computed, type Ref } from 'vue';
import { useQuery } from '@tanstack/vue-query';
import { api } from '@/packages/api/src';
import type { InvoiceIndexEntry, InvoiceRecipient } from '@/packages/api/src';
import { getCurrentOrganizationId } from '@/utils/useUser';

type InvoiceSetting = Awaited<ReturnType<typeof api.getInvoiceSettings>>['data'];

/**
 * Invoices are paginated server-side; `status` filters to one of
 * draft/sent/paid/cancelled (undefined = all).
 */
export function useInvoicesQuery(status: Ref<string | undefined>, page: Ref<number>) {
    const query = useQuery({
        queryKey: computed(() => ['invoices', getCurrentOrganizationId(), status.value, page.value]),
        queryFn: async () => {
            const organizationId = getCurrentOrganizationId();
            if (!organizationId) throw new Error('No organization');
            const response = await api.getInvoices({
                params: { organization: organizationId },
                queries: {
                    page: page.value,
                    status: status.value as 'draft' | 'sent' | 'paid' | 'cancelled' | undefined,
                },
            });
            return { data: response.data, meta: response.meta as { total?: number } | undefined };
        },
        enabled: () => !!getCurrentOrganizationId(),
        staleTime: 1000 * 10,
    });

    const invoices = computed<InvoiceIndexEntry[]>(() => query.data.value?.data ?? []);
    const total = computed<number>(() => query.data.value?.meta?.total ?? 0);

    return { ...query, invoices, total };
}

export function useInvoiceRecipientsQuery() {
    const query = useQuery({
        queryKey: computed(() => ['invoice-recipients', getCurrentOrganizationId()]),
        queryFn: async () => {
            const organizationId = getCurrentOrganizationId();
            if (!organizationId) throw new Error('No organization');
            const response = await api.getInvoiceRecipients({
                params: { organization: organizationId },
            });
            return response.data;
        },
        enabled: () => !!getCurrentOrganizationId(),
        staleTime: 1000 * 10,
    });

    const recipients = computed<InvoiceRecipient[]>(() => query.data.value ?? []);

    return { ...query, recipients };
}

export function useInvoiceSettingsQuery() {
    const query = useQuery({
        queryKey: computed(() => ['invoice-settings', getCurrentOrganizationId()]),
        queryFn: async () => {
            const organizationId = getCurrentOrganizationId();
            if (!organizationId) throw new Error('No organization');
            const response = await api.getInvoiceSettings({
                params: { organization: organizationId },
            });
            return response.data;
        },
        enabled: () => !!getCurrentOrganizationId(),
        staleTime: 1000 * 60,
    });

    const settings = computed<InvoiceSetting | undefined>(() => query.data.value);

    return { ...query, settings };
}
