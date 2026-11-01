import CustomerStory from "../../src/components/pages/CustomerStory";
import { customerStoryMeta } from "../seo";
import { findCustomerStory } from "../../src/lib/customerStories";

export function meta({ params }) {
  return customerStoryMeta(findCustomerStory(params.slug));
}

export default CustomerStory;
