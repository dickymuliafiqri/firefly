import { useParallax } from '@/lib/parallax';
import moonSvg from '@/assets/moon.svg';

const starPositions: readonly [left: string, top: string, d: string][] = [
  ['6%', '12%', '0s'], ['14%', '32%', '1.2s'], ['22%', '8%', '2.1s'], ['30%', '24%', '0.6s'],
  ['38%', '14%', '1.7s'], ['47%', '6%', '0.3s'], ['55%', '20%', '2.6s'], ['63%', '11%', '1.1s'],
  ['71%', '28%', '0.9s'], ['85%', '18%', '2.3s'], ['92%', '9%', '1.5s'], ['9%', '44%', '2.8s'],
  ['26%', '38%', '0.4s'], ['44%', '33%', '1.9s'], ['59%', '40%', '2.9s'], ['78%', '38%', '2.4s'],
  ['12%', '58%', '0.8s'], ['64%', '52%', '1.4s'], ['38%', '62%', '2.2s'], ['88%', '48%', '0.5s'],
];

/**
 * Sky global — satu instance di level shell (Keputusan B, DESIGN_RULES §7).
 * Maksimum 3 elemen parallax: bintang depth:3, bulan depth:-9.
 */
export function SkyBackground() {
  const starsRef = useParallax<HTMLDivElement>(3, 'stars');
  const moonRef = useParallax<HTMLDivElement>(-9, 'moon');

  return (
    <div className="sky-global" aria-hidden="true">
      <div className="sky-star-wrapper" ref={starsRef}>
        {starPositions.map(([left, top, d], i) => (
          <i key={i} style={{ left, top, ['--d' as string]: d }} />
        ))}
      </div>
      <div className="sky-moon" ref={moonRef}>
        <img src={moonSvg} alt="" width={120} height={120} />
      </div>
      <div className="sky-horizonlight" />
    </div>
  );
}

