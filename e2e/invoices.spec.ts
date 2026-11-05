import { expect, test } from '../playwright/fixtures';
import { PLAYWRIGHT_BASE_URL } from '../playwright/config';

test('empty invoice state stays visible on a phone viewport', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`${PLAYWRIGHT_BASE_URL}/invoices`);

    const emptyState = page.getByTestId('invoice_empty_state');
    await expect(emptyState).toBeVisible();
    await expect(page.getByTestId('invoices_table')).toHaveCount(0);

    const bounds = await emptyState.boundingBox();
    expect(bounds).not.toBeNull();
    expect(bounds!.x).toBeGreaterThanOrEqual(0);
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(390);
    await expect(emptyState.getByRole('heading', { name: 'No invoices yet' })).toBeVisible();
});
