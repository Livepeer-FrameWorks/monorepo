import { client } from '../client.js';
import { 
  GetBillingTiersDocument,
  GetInvoicesDocument,
  GetInvoiceDocument,
  GetBillingStatusDocument,
  GetUsageRecordsDocument,
  CreatePaymentDocument,
  UpdateBillingTierDocument
} from '../generated/apollo-helpers';

export const billingService = {
  // Queries
  async getBillingTiers() {
    const result = await client.query({
      query: GetBillingTiersDocument,
      fetchPolicy: 'cache-first'
    });
    return result.data.billingTiers;
  },

  async getInvoices() {
    const result = await client.query({
      query: GetInvoicesDocument,
      fetchPolicy: 'cache-first'
    });
    return result.data.invoices;
  },

  async getInvoice(id) {
    const result = await client.query({
      query: GetInvoiceDocument,
      variables: { id },
      fetchPolicy: 'cache-first'
    });
    return result.data.invoice;
  },

  async getBillingStatus() {
    const result = await client.query({
      query: GetBillingStatusDocument,
      fetchPolicy: 'cache-first'
    });
    return result.data.billingStatus;
  },

  async getUsageRecords(timeRange) {
    const result = await client.query({
      query: GetUsageRecordsDocument,
      variables: { timeRange },
      fetchPolicy: 'cache-first'
    });
    return result.data.usageRecords;
  },

  // Mutations
  async createPayment(input) {
    const result = await client.mutate({
      mutation: CreatePaymentDocument,
      variables: { input },
      refetchQueries: [
        { query: GetInvoicesDocument },
        { query: GetBillingStatusDocument }
      ]
    });
    return result.data.createPayment;
  },

  async updateBillingTier(tierId) {
    const result = await client.mutate({
      mutation: UpdateBillingTierDocument,
      variables: { tierId },
      refetchQueries: [{ query: GetBillingStatusDocument }]
    });
    return result.data.updateBillingTier;
  }
};