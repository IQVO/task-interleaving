Feature: The interleaving policy of a site
  A site is inert until an operator declares its policy. The policy is versioned:
  the first declaration sends no version, every later one must send the current
  version, and every declaration is announced to analytics.

  Scenario: A site that never declared a policy has none
    Then site "SITE-1" has no policy

  Scenario: Declaring a policy stores version 1 and announces it
    When site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "REPLENISH,PICK" and vertical weight 1.5
    Then the command succeeds
    And the policy is at version 1 with 3 candidates and a 90 s hint ttl
    And the policy has vertical weight 1.5 and task types "PICK,REPLENISH"
    And the published events are "InterleavingPolicyDeclared"

  Scenario: The vertical weight defaults to 1
    When site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    Then the policy has vertical weight 1 and task types "PICK"

  Scenario: A declared policy can be read back
    Given site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    When I read the policy of site "SITE-1"
    Then the policy is at version 1 with 3 candidates and a 90 s hint ttl

  Scenario: Replacing a policy needs its current version
    Given site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    When I declare the policy of site "SITE-1" expecting version 1 with max detour 40 m, up to 2 candidates and a 60 s hint ttl for "PICK,PACK"
    Then the policy is at version 2 with 2 candidates and a 60 s hint ttl
    And the published events are "InterleavingPolicyDeclared,InterleavingPolicyDeclared"

  Scenario: A stale version is refused and changes nothing
    Given site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    When I declare the policy of site "SITE-1" expecting version 7 with max detour 40 m, up to 2 candidates and a 60 s hint ttl for "PICK"
    Then the command fails with "if-match"
    When I read the policy of site "SITE-1"
    Then the policy is at version 1 with 3 candidates and a 90 s hint ttl
    And the published events are "InterleavingPolicyDeclared"

  Scenario: Declaring over an existing policy without a version is refused
    Given site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    When I declare the policy of site "SITE-1" with max detour 40 m, up to 2 candidates and a 60 s hint ttl for "PICK"
    Then the command fails with "if-match"

  Scenario: A version for a policy that does not exist is refused
    When I declare the policy of site "SITE-1" expecting version 1 with max detour 40 m, up to 2 candidates and a 60 s hint ttl for "PICK"
    Then the command fails with "if-match"
    And site "SITE-1" has no policy

  Scenario Outline: An invalid policy is refused and nothing is announced
    When I declare the policy of site "SITE-1" with max detour <detour> m, up to <candidates> candidates and a <ttl> s hint ttl for "<types>"
    Then the command fails with "<message>"
    And no event is published

    Examples:
      | detour | candidates | ttl | types    | message                |
      | 0      | 3          | 90  | PICK     | max detour             |
      | 25     | 0          | 90  | PICK     | max candidates         |
      | 25     | 6          | 90  | PICK     | max candidates         |
      | 25     | 3          | 0   | PICK     | hint ttl               |
      | 25     | 3          | 90  | PICK,FLY | eligible task types    |

  Scenario: A policy needs eligible task types
    When I declare the policy of site "SITE-1" for task types ""
    Then the command fails with "eligible task types"

  Scenario: A policy needs a valid site id
    When I declare the policy of site "SITE/1" with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    Then the command fails with "invalid site id"

  Scenario: Every site has its own policy
    Given site "SITE-1" declares a policy with max detour 25 m, up to 3 candidates and a 90 s hint ttl for "PICK"
    Then site "SITE-2" has no policy
