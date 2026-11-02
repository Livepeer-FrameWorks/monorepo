import { Navigate, useParams } from "react-router";
import config from "../../config";
import { findCustomerStory } from "../../lib/customerStories";
import { signupUrl } from "@/lib/siteLinks";
import { contactPath } from "@/lib/contactTopics";
import { Section, SectionContainer } from "@/components/ui/section";
import {
  MarketingHero,
  MarketingBand,
  HeadlineStack,
  MarketingFeatureWall,
  MarketingFinalCTA,
  SectionDivider,
} from "@/components/marketing";

const StoryBand = ({ eyebrow, title, children }) => (
  <>
    <Section>
      <SectionContainer>
        <MarketingBand preset="quiet">
          <HeadlineStack eyebrow={eyebrow} title={title} align="left" underlineAlign="start" />
          {children}
        </MarketingBand>
      </SectionContainer>
    </Section>
    <SectionDivider />
  </>
);

const Paragraphs = ({ items }) => (
  <div className="space-y-4 max-w-3xl text-muted-foreground">
    {items.map((paragraph) => (
      <p key={paragraph}>{paragraph}</p>
    ))}
  </div>
);

const CustomerStory = () => {
  const { slug } = useParams();
  const story = findCustomerStory(slug);

  if (!story) {
    return <Navigate to="/showcase" replace />;
  }

  return (
    <div className="pt-16">
      <MarketingHero
        seed={`/customers/${story.slug}`}
        eyebrow={story.customer}
        title={story.headline}
        description={story.summary}
        align="center"
        surface="gradient"
        surfaceTone="accent"
        surfaceIntensity="raised"
      />

      <SectionDivider />

      <StoryBand eyebrow="The situation" title={`What ${story.customer} needed`}>
        <Paragraphs items={story.situation} />
      </StoryBand>

      <StoryBand eyebrow="The approach" title="How we worked on it">
        <Paragraphs items={story.approach} />
      </StoryBand>

      <StoryBand eyebrow="The setup" title="What runs on FrameWorks">
        <MarketingFeatureWall
          items={story.setup.map(({ title, description }) => ({
            title,
            description,
            hover: "subtle",
            stripe: true,
          }))}
          columns={3}
          stackAt="md"
        />
      </StoryBand>

      <StoryBand eyebrow="The results" title="What changed">
        <MarketingFeatureWall
          items={story.results.map(({ value, label }) => ({
            title: value,
            description: label,
            hover: "subtle",
            stripe: true,
          }))}
          columns={3}
          stackAt="md"
        />
      </StoryBand>

      {story.quote ? (
        <StoryBand eyebrow={story.quote.author} title={story.quote.role}>
          <blockquote className="max-w-3xl text-lg text-foreground">{story.quote.text}</blockquote>
        </StoryBand>
      ) : null}

      <Section>
        <MarketingFinalCTA
          eyebrow="Next steps"
          title="Talk to the engineers"
          description="Tell us about your workflow. We help with the whole media pipeline, including the parts before and after FrameWorks."
          variant="band"
          primaryAction={{ label: "Talk to the engineers", to: contactPath("engineering") }}
          secondaryAction={[
            {
              label: "Start Free",
              href: signupUrl(config.appUrl, `customer-${story.slug}`),
              external: true,
            },
          ]}
        />
      </Section>
    </div>
  );
};

export default CustomerStory;
