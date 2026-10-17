import type {ReactNode} from 'react';
import clsx from 'clsx';
import Link from '@docusaurus/Link';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Layout from '@theme/Layout';
import Heading from '@theme/Heading';

import styles from './index.module.css';

function StudyDisclaimer() {
  return (
    <div
      style={{
        background: '#fef3c7',
        color: '#78350f',
        textAlign: 'center',
        padding: '0.6rem 1rem',
        fontSize: '0.9rem',
        borderBottom: '1px solid #f59e0b',
      }}>
      ⚠️ <strong>Study project</strong> — an educational DDD exercise. Not a
      production system.
    </div>
  );
}

function HomepageHeader() {
  const {siteConfig} = useDocusaurusContext();
  return (
    <header className={clsx('hero', styles.heroBanner)}>
      <StudyDisclaimer />
      <div className="container">
        <p className={styles.eyebrow}>
          warehouse-systems · WES tier · Supporting subdomain
        </p>
        <Heading as="h1" className={styles.heroTitle}>
          {siteConfig.title}
        </Heading>
        <p className={styles.heroSubtitle}>{siteConfig.tagline}</p>
        <p className={styles.heroLead}>
          Two aggregates, the StationTrip (one per station) and the
          InterleavingPolicy (one per site), and a pure recommender that ranks
          the open tasks of a site by rectilinear travel distance from where the
          operator now stands. The result is an expiring hint, never an
          assignment. Today the domain, the contracts and the architecture
          tests are on develop; the service itself is still being built.
        </p>
        <div className={styles.buttons}>
          <Link className="button button--primary button--lg" to="/docs/overview/introduction">
            Read the docs
          </Link>
          <Link
            className="button button--secondary button--lg"
            to="/docs/api-reference">
            API Reference
          </Link>
          <Link
            className="button button--secondary button--lg"
            to="/docs/adr">
            ADRs
          </Link>
        </div>
      </div>
    </header>
  );
}

export default function Home(): ReactNode {
  const {siteConfig} = useDocusaurusContext();
  return (
    <Layout
      title={siteConfig.title}
      description="Documentation for the Task Interleaving bounded context: the StationTrip and InterleavingPolicy aggregates, the rectilinear-v1 recommender, advisory hints and the task-interleaving event stream.">
      <HomepageHeader />
      <main>
        <section className={styles.invariant}>
          <div className="container">
            <blockquote className={styles.invariantQuote}>
              A hint is <strong>advisory</strong>: it lists at most five nearby
              open tasks, expires on a TTL, never assigns work, and a station
              has <strong>at most one Active hint</strong>.
            </blockquote>
            <p className={styles.invariantCaption}>
              <Link to="/docs/ddd/aggregate-design-canvas">
                The StationTrip aggregate →
              </Link>
            </p>
          </div>
        </section>
      </main>
    </Layout>
  );
}
