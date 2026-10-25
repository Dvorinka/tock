import { computed, type Ref } from 'vue';
import { useQuery } from '@tanstack/vue-query';
import { api } from '@/packages/api/src';
import type { Audit } from '@/packages/api/src';
import { getCurrentOrganizationId } from '@/utils/useUser';

export type AuditFilters = {
    model?: string;
    event?: string;
    actor?: string;
    from?: string;
    to?: string;
};

/**
 * The audit log is paginated server-side (15 per page), so unlike the other
 * resource hooks this one does not walk every page.
 */
export function useAuditsQuery(filters: Ref<AuditFilters>, page: Ref<number>) {
    const query = useQuery({
        queryKey: computed(() => ['audits', getCurrentOrganizationId(), filters.value, page.value]),
        queryFn: async () => {
            const organizationId = getCurrentOrganizationId();
            if (!organizationId) throw new Error('No organization');
            const response = await api.getAudits({
                params: { organization: organizationId },
                queries: {
                    page: page.value,
                    actor: filters.value.actor || undefined,
                    model: filters.value.model || undefined,
                    event: filters.value.event || undefined,
                    from: filters.value.from || undefined,
                    to: filters.value.to || undefined,
                },
            });
            return { data: response.data, meta: response.meta };
        },
        enabled: () => !!getCurrentOrganizationId(),
        staleTime: 1000 * 10,
    });

    const audits = computed<Audit[]>(() => query.data.value?.data ?? []);
    const total = computed<number>(() => query.data.value?.meta.total ?? 0);

    return { ...query, audits, total };
}
