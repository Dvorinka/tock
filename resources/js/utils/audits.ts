import type { Audit } from '@/packages/api/src';

/**
 * Filter options for the audit log. The values are the `auditable_type` morph
 * aliases stored in the database; the labels mirror the server-side map.
 */
export const auditModelOptions = [
    { value: 'time-entry', label: 'Time entry' },
    { value: 'project', label: 'Project' },
    { value: 'project-member', label: 'Project member' },
    { value: 'task', label: 'Task' },
    { value: 'client', label: 'Client' },
    { value: 'tag', label: 'Tag' },
    { value: 'membership', label: 'Member' },
    { value: 'organization-invitation', label: 'Invitation' },
    { value: 'organization', label: 'Organization' },
    { value: 'user', label: 'User' },
    { value: 'invoice', label: 'Invoice' },
    { value: 'invoice-recipient', label: 'Invoice recipient' },
];

export const auditEventOptions = [
    { value: 'created', label: 'Created' },
    { value: 'updated', label: 'Updated' },
    { value: 'deleted', label: 'Deleted' },
];

export type AuditChange = {
    field: string;
    oldValue: string;
    newValue: string;
};

/** The JSON columns hold an object per audit, or `[]` for an empty side. */
export function auditValuesToObject(values: Audit['old_values']): Record<string, unknown> {
    if (Array.isArray(values) || values === null || values === undefined) {
        return {};
    }
    return values as Record<string, unknown>;
}

export function auditChanges(audit: Audit): AuditChange[] {
    const before = auditValuesToObject(audit.old_values);
    const after = auditValuesToObject(audit.new_values);
    const fields = Array.from(new Set([...Object.keys(before), ...Object.keys(after)])).sort();
    return fields.map((field) => ({
        field,
        oldValue: formatAuditValue(before[field], field in before),
        newValue: formatAuditValue(after[field], field in after),
    }));
}

function formatAuditValue(value: unknown, present: boolean): string {
    if (!present) {
        return '—';
    }
    if (value === null) {
        return 'null';
    }
    if (typeof value === 'object') {
        return JSON.stringify(value);
    }
    return String(value);
}

export function auditEventDotClass(event: string): string {
    switch (event) {
        case 'created':
            return 'bg-green-500';
        case 'deleted':
            return 'bg-destructive';
        default:
            return 'bg-blue-500';
    }
}

/** Short timestamp for the table: `2026-03-01 14:05` (the API sends ISO). */
export function formatAuditTimestamp(value: string): string {
    return value.slice(0, 16).replace('T', ' ');
}

/** `archived_at` reads better as `Archived at` in the changes column. */
export function auditFieldLabel(field: string): string {
    const spaced = field.replace(/_/g, ' ');
    return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}
