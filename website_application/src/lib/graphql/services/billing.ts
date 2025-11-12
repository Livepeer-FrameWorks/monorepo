import { client } from "../client.js";
import {
  GetBillingTiersDocument,
  GetInvoicesDocument,
  GetInvoiceDocument,
  GetBillingStatusDocument,
  GetUsageRecordsDocument,
  CreatePaymentDocument,
} from "../generated/apollo-helpers";
import type {
  GetBillingTiersQuery,
  GetInvoicesQuery,
  GetInvoiceQuery,
  GetInvoiceQueryVariables,
  GetBillingStatusQuery,
  GetUsageRecordsQuery,
  GetUsageRecordsQueryVariables,
  CreatePaymentMutation,
  CreatePaymentMutationVariables,
} from "../generated/types";

export const billingService = {
  // Queries
  async getBillingTiers(): Promise<GetBillingTiersQuery["billingTiers"]> {
    const result = await client.query({
      query: GetBillingTiersDocument,
      fetchPolicy: "cache-first",
    });
    return result.data.billingTiers;
  },

  async getInvoices(): Promise<GetInvoicesQuery["invoices"]> {
    const result = await client.query({
      query: GetInvoicesDocument,
      fetchPolicy: "cache-first",
    });
    return result.data.invoices;
  },

  async getInvoice(id: GetInvoiceQueryVariables["id"]): Promise<GetInvoiceQuery["invoice"]> {
    const result = await client.query({
      query: GetInvoiceDocument,
      variables: { id },
      fetchPolicy: "cache-first",
    });
    return result.data.invoice;
  },

  async getBillingStatus(): Promise<GetBillingStatusQuery["billingStatus"]> {
    const result = await client.query({
      query: GetBillingStatusDocument,
      fetchPolicy: "cache-first",
    });
    return result.data.billingStatus;
  },

  async getUsageRecords(
    timeRange: GetUsageRecordsQueryVariables["timeRange"],
  ): Promise<GetUsageRecordsQuery["usageRecords"]> {
    const result = await client.query({
      query: GetUsageRecordsDocument,
      variables: { timeRange },
      fetchPolicy: "cache-first",
    });
    return result.data.usageRecords;
  },

  // Mutations
  async createPayment(
    input: CreatePaymentMutationVariables["input"],
  ): Promise<CreatePaymentMutation["createPayment"]> {
    const result = await client.mutate({
      mutation: CreatePaymentDocument,
      variables: { input },
      refetchQueries: [
        { query: GetInvoicesDocument },
        { query: GetBillingStatusDocument },
      ],
    });
    return result.data!.createPayment;
  },

};
